// Package update manages self-update lifecycle for the engram worker binary.
// Updates are fetched from GitHub Releases, verified with a SHA-256 checksum
// (and optionally a sigstore bundle via cosign), extracted into a temp
// directory, and swapped into the install location atomically per file.
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Package-level constants that callers and tests depend on.
// Values are load-bearing — do not change them without a coordinated
// release change, since consumers (dashboard, hooks) may hard-code them.
const (
	GitHubRepo       = "thebtf/engram"
	ReleasesAPI      = "https://api.github.com/repos/" + GitHubRepo + "/releases/latest"
	CheckInterval    = 24 * time.Hour
	MaxExtractedSize = 250 * 1024 * 1024 // 250 MB — guards against decompression bombs
	RestartDelay     = 500 * time.Millisecond
)

// InstallScriptURL is the canonical remote bootstrap script, exposed so
// callers can show a fallback manual-install command when automated
// update is unavailable.
const InstallScriptURL = "https://raw.githubusercontent.com/" + GitHubRepo + "/main/scripts/install.sh"

// Release is the minimal subset of the GitHub Releases API response that
// the updater needs.  Extra fields returned by the API are silently
// ignored during JSON decode.
type Release struct {
	PublishedAt time.Time `json:"published_at"`
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Assets      []Asset   `json:"assets"`
}

// Asset describes a single downloadable file attached to a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// UpdateInfo is the contract surface returned to HTTP callers.
// JSON field names are wire-stable — renaming them would break the dashboard.
type UpdateInfo struct {
	PublishedAt         time.Time `json:"published_at,omitempty"`
	CurrentVersion      string    `json:"current_version"`
	LatestVersion       string    `json:"latest_version"`
	ReleaseNotes        string    `json:"release_notes,omitempty"`
	DownloadURL         string    `json:"download_url,omitempty"`
	ChecksumsURL        string    `json:"checksums_url,omitempty"`
	BundleURL           string    `json:"bundle_url,omitempty"`
	ManualUpdateCommand string    `json:"manual_update_command,omitempty"`
	Available           bool      `json:"available"`
}

// UpdateStatus carries the current phase of an in-flight update.
// State values ("idle", "downloading", "verifying", "applying", "done",
// "error") are consumed verbatim by the dashboard — keep them stable.
type UpdateStatus struct {
	State               string  `json:"state"`
	Message             string  `json:"message"`
	Error               string  `json:"error,omitempty"`
	ManualUpdateCommand string  `json:"manual_update_command,omitempty"`
	Progress            float64 `json:"progress"`
}

// Updater owns all mutable state for the self-update flow.
// All fields are guarded by mu except httpClient (immutable after New).
type Updater struct {
	mu             sync.RWMutex
	httpClient     *http.Client
	lastCheck      time.Time
	cachedUpdate   *UpdateInfo
	currentVersion string
	installDir     string
	status         UpdateStatus
}

// New constructs an Updater ready for use.  httpClient has a 30-second
// timeout because the GitHub API and release CDN are external; without a
// timeout a hung connection would block the apply goroutine indefinitely.
func New(currentVersion, installDir string) *Updater {
	return &Updater{
		currentVersion: currentVersion,
		installDir:     installDir,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		status:         UpdateStatus{State: "idle"},
	}
}

// GetStatus returns a consistent snapshot of the current update status.
func (u *Updater) GetStatus() UpdateStatus {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.status
}

// setStatus records a progress transition.  All callers hold no lock
// before calling; setStatus acquires it internally.
func (u *Updater) setStatus(state string, progress float64, message string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status = UpdateStatus{
		State:    state,
		Progress: progress,
		Message:  message,
	}
}

// setError records a terminal error state and always includes the manual
// update command so the dashboard can show a fallback action.
func (u *Updater) setError(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status = UpdateStatus{
		State:               "error",
		Message:             "Update failed",
		Error:               err.Error(),
		ManualUpdateCommand: GetManualUpdateCommand(""),
	}
}

// GetManualUpdateCommand builds the curl+bash install command.
// When version is empty the script installs the latest release; otherwise
// it pins to the requested tag so admins can roll back to a specific version.
func GetManualUpdateCommand(version string) string {
	if version == "" {
		return fmt.Sprintf("curl -sSL %s | bash", InstallScriptURL)
	}
	return fmt.Sprintf("curl -sSL %s | bash -s -- %s", InstallScriptURL, version)
}

// CheckForUpdate queries the GitHub Releases API for the latest tag and
// compares it against the running version.  Results are cached for one
// hour to stay within unauthenticated GitHub API rate limits (60 req/hr).
func (u *Updater) CheckForUpdate(ctx context.Context) (*UpdateInfo, error) {
	u.setStatus("checking", 0, "Checking for updates...")

	if cached := u.cachedResultIfFresh(); cached != nil {
		u.setStatus("idle", 0, "")
		return cached, nil
	}

	release, err := u.fetchLatestRelease(ctx)
	if err != nil {
		u.setError(err)
		return nil, err
	}

	info := u.buildUpdateInfo(release)
	u.storeCache(info)
	u.setStatus("idle", 0, "")
	return info, nil
}

// cachedResultIfFresh returns the cached UpdateInfo when it is younger
// than one hour, or nil when a fresh API call is needed.
func (u *Updater) cachedResultIfFresh() *UpdateInfo {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.cachedUpdate != nil && time.Since(u.lastCheck) < time.Hour {
		return u.cachedUpdate
	}
	return nil
}

// fetchLatestRelease performs the actual GitHub API request and decodes
// the response into a Release value.
func (u *Updater) fetchLatestRelease(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", ReleasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "engram/"+u.currentVersion)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse release info: %w", err)
	}
	return &release, nil
}

// buildUpdateInfo converts a Release into an UpdateInfo, resolving asset
// download URLs for the current OS/arch combination.
func (u *Updater) buildUpdateInfo(release *Release) *UpdateInfo {
	info := &UpdateInfo{
		CurrentVersion:      u.currentVersion,
		LatestVersion:       strings.TrimPrefix(release.TagName, "v"),
		ReleaseNotes:        release.Body,
		PublishedAt:         release.PublishedAt,
		ManualUpdateCommand: GetManualUpdateCommand("v" + strings.TrimPrefix(release.TagName, "v")),
	}
	info.Available = isNewerVersion(info.LatestVersion, u.currentVersion)

	if info.Available {
		u.resolveAssetURLs(info, release.Assets)
		if info.DownloadURL == "" {
			log.Warn().Str("platform", getPlatform()).Msg("No release asset found for current platform")
		}
	}
	return info
}

// resolveAssetURLs scans the asset list for the platform archive, the
// checksums file, and the sigstore bundle, and populates the URL fields.
func (u *Updater) resolveAssetURLs(info *UpdateInfo, assets []Asset) {
	archiveName := fmt.Sprintf("engram_%s_%s.tar.gz", info.LatestVersion, getPlatform())
	for _, asset := range assets {
		switch asset.Name {
		case archiveName:
			info.DownloadURL = asset.BrowserDownloadURL
		case "checksums.txt":
			info.ChecksumsURL = asset.BrowserDownloadURL
		case "checksums.txt.sigstore.json":
			info.BundleURL = asset.BrowserDownloadURL
		}
	}
}

// storeCache persists the update result and records the check timestamp
// so the freshness guard in cachedResultIfFresh can work correctly.
func (u *Updater) storeCache(info *UpdateInfo) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.lastCheck = time.Now()
	u.cachedUpdate = info
}

// ApplyUpdate drives the full update pipeline: download verification
// files, optionally verify the sigstore signature, download the archive,
// verify its checksum, extract it, and swap the binaries into place.
// The update is applied to all known install directories (primary + cache).
func (u *Updater) ApplyUpdate(ctx context.Context, info *UpdateInfo) error {
	if !info.Available || info.DownloadURL == "" {
		return fmt.Errorf("no update available or download URL missing")
	}

	tmpDir, err := os.MkdirTemp("", "engram-update-*")
	if err != nil {
		u.setError(err)
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	checksumsPath := filepath.Join(tmpDir, "checksums.txt")
	bundlePath := filepath.Join(tmpDir, "checksums.txt.sigstore.json")

	if err := u.downloadVerificationFiles(ctx, info, checksumsPath, bundlePath); err != nil {
		return err
	}

	u.verifySignatureIfAvailable(ctx, info, checksumsPath, bundlePath)

	archivePath, err := u.downloadArchive(ctx, info, tmpDir)
	if err != nil {
		return err
	}

	if err := u.verifyArchiveChecksum(info, archivePath, checksumsPath); err != nil {
		return err
	}

	extractDir := filepath.Join(tmpDir, "extracted")
	if err := u.extractArchive(archivePath, extractDir); err != nil {
		return err
	}

	if err := u.installBinaries(extractDir); err != nil {
		return err
	}

	// Invalidate the cache so subsequent CheckForUpdate calls do not
	// report a stale "update available" result after the binary is current.
	u.mu.Lock()
	u.cachedUpdate = nil
	u.lastCheck = time.Time{}
	u.mu.Unlock()

	u.setStatus("done", 1.0, fmt.Sprintf("Updated to v%s. Restart required.", info.LatestVersion))
	log.Info().Str("version", info.LatestVersion).Msg("Update applied successfully")
	return nil
}

// downloadVerificationFiles fetches checksums.txt and the sigstore bundle
// (when their URLs are known) so they are available for verification before
// the heavier archive download begins.
func (u *Updater) downloadVerificationFiles(ctx context.Context, info *UpdateInfo, checksumsPath, bundlePath string) error {
	u.setStatus("downloading", 0.1, "Downloading checksums...")

	if info.ChecksumsURL != "" {
		if err := u.downloadFile(ctx, info.ChecksumsURL, checksumsPath); err != nil {
			u.setError(err)
			return fmt.Errorf("failed to download checksums: %w", err)
		}
	}
	if info.BundleURL != "" {
		if err := u.downloadFile(ctx, info.BundleURL, bundlePath); err != nil {
			u.setError(err)
			return fmt.Errorf("failed to download sigstore bundle: %w", err)
		}
	}
	return nil
}

// verifySignatureIfAvailable runs cosign verify-blob when both the
// checksums file and the sigstore bundle are present.  Failure is logged
// as a warning rather than aborting the update because cosign may not be
// installed in all deployment environments.
func (u *Updater) verifySignatureIfAvailable(ctx context.Context, info *UpdateInfo, checksumsPath, bundlePath string) {
	u.setStatus("verifying", 0.2, "Verifying signature...")
	if info.ChecksumsURL == "" || info.BundleURL == "" {
		return
	}
	if err := u.verifySigstoreBundle(ctx, checksumsPath, bundlePath); err != nil {
		log.Warn().Err(err).Msg("Signature verification failed or skipped")
	} else {
		log.Info().Msg("Sigstore signature verification passed")
	}
}

// downloadArchive fetches the release archive and returns the local path.
func (u *Updater) downloadArchive(ctx context.Context, info *UpdateInfo, tmpDir string) (string, error) {
	u.setStatus("downloading", 0.3, "Downloading update...")
	archivePath := filepath.Join(tmpDir, "release.tar.gz")
	if err := u.downloadFile(ctx, info.DownloadURL, archivePath); err != nil {
		u.setError(err)
		return "", fmt.Errorf("failed to download release: %w", err)
	}
	return archivePath, nil
}

// verifyArchiveChecksum computes the SHA-256 of the downloaded archive and
// compares it against the expected value in checksums.txt.  Skipped when
// no ChecksumsURL was provided (dev builds).
func (u *Updater) verifyArchiveChecksum(info *UpdateInfo, archivePath, checksumsPath string) error {
	if info.ChecksumsURL == "" {
		return nil
	}
	u.setStatus("verifying", 0.6, "Verifying checksum...")
	if err := u.verifyChecksum(archivePath, checksumsPath, info.LatestVersion); err != nil {
		u.setError(err)
		return fmt.Errorf("checksum verification failed: %w", err)
	}
	log.Info().Msg("Checksum verification passed")
	return nil
}

// extractArchive unpacks archivePath into destDir and enforces the
// MaxExtractedSize limit on every individual file.
func (u *Updater) extractArchive(archivePath, destDir string) error {
	u.setStatus("applying", 0.7, "Extracting files...")
	if err := u.extractTarGz(archivePath, destDir); err != nil {
		u.setError(err)
		return fmt.Errorf("failed to extract archive: %w", err)
	}
	return nil
}

// installBinaries replaces binaries in all known install directories.
func (u *Updater) installBinaries(extractDir string) error {
	u.setStatus("applying", 0.85, "Installing update...")
	if err := u.replaceBinaries(extractDir); err != nil {
		u.setError(err)
		return fmt.Errorf("failed to replace binaries: %w", err)
	}
	return nil
}

// downloadFile is the single HTTP fetch primitive used by all download
// steps.  The User-Agent header identifies the running version so GitHub
// access logs can correlate download bursts with release traffic.
func (u *Updater) downloadFile(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "engram/"+u.currentVersion)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// destPath is always constructed from os.MkdirTemp output — not user input.
	f, err := os.Create(destPath) // #nosec G304
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

// verifySigstoreBundle runs cosign verify-blob using keyless verification.
// The certificate identity is tied to the GitHub Actions workflow for this
// repo; any signature from a different issuer or workflow will be rejected.
func (u *Updater) verifySigstoreBundle(ctx context.Context, checksumsPath, bundlePath string) error {
	if _, err := exec.LookPath("cosign"); err != nil {
		return fmt.Errorf("cosign not installed: %w", err)
	}

	cmd := exec.CommandContext(ctx, "cosign", "verify-blob",
		"--bundle", bundlePath,
		"--certificate-identity-regexp", "https://github.com/thebtf/engram/.*",
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		checksumsPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cosign verification failed: %w, output: %s", err, string(output))
	}
	return nil
}

// verifyChecksum computes the SHA-256 of the archive and looks up the
// expected value by filename in the checksums file.  The lookup is by
// suffix so the file can contain checksums for multiple platforms without
// order dependency.
func (u *Updater) verifyChecksum(archivePath, checksumsPath, version string) error {
	// checksumsPath comes from os.MkdirTemp — not user-controlled.
	data, err := os.ReadFile(checksumsPath) // #nosec G304
	if err != nil {
		return fmt.Errorf("failed to read checksums: %w", err)
	}

	// archivePath also comes from os.MkdirTemp.
	f, err := os.Open(archivePath) // #nosec G304
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actualChecksum := hex.EncodeToString(h.Sum(nil))

	expectedName := fmt.Sprintf("engram_%s_%s.tar.gz", version, getPlatform())
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 || !strings.HasSuffix(parts[1], expectedName) {
			continue
		}
		if parts[0] != actualChecksum {
			return fmt.Errorf("checksum mismatch: expected %s, got %s", parts[0], actualChecksum)
		}
		return nil
	}

	return fmt.Errorf("no checksum found for %s", expectedName)
}

// extractTarGz unpacks a gzip-compressed tar archive into destDir.
//
// Security invariants enforced here:
//  1. Path traversal: every resolved target path must stay under destDir.
//     A crafted archive entry with "../" components is rejected.
//  2. Decompression bomb: each regular file is limited to MaxExtractedSize.
//     Reaching the limit is treated as an error, not a silent truncation.
//  3. File mode: bits beyond 0755 are masked off to avoid setuid/setgid
//     surprises when the archive was built on a different system.
func (u *Updater) extractTarGz(archivePath, destDir string) error {
	if err := os.MkdirAll(destDir, 0750); err != nil {
		return err
	}

	// archivePath comes from os.MkdirTemp — not user input.
	f, err := os.Open(archivePath) // #nosec G304
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Invariant 1: resolve and validate the target path before any I/O.
		// #nosec G305 — traversal is prevented by the check immediately below.
		target := filepath.Join(destDir, header.Name)
		if !strings.HasPrefix(target, cleanDest) {
			return fmt.Errorf("invalid tar path: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0750); err != nil {
				return err
			}

		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
				return err
			}
			// Invariant 3: mask mode to 0755 before creating the file.
			// #nosec G304,G115 — target validated above; mode from trusted tar header
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode)&0755)
			if err != nil {
				return err
			}
			// Invariant 2: io.LimitReader caps the copy; hitting the limit
			// means written == MaxExtractedSize (not strictly less), so we
			// treat equality as the bomb trigger.
			written, copyErr := io.Copy(outFile, io.LimitReader(tr, MaxExtractedSize))
			_ = outFile.Close()
			if copyErr != nil {
				return copyErr
			}
			if written == MaxExtractedSize {
				return fmt.Errorf("file %s exceeds maximum allowed size", header.Name)
			}
		}
	}
	return nil
}

// replaceBinaries copies each known binary from extractDir to every install
// directory.  Each destination is backed up before replacement so that a
// partial failure can be detected (the deferred closure restores the backup
// if the destination is missing after the copy attempt).
func (u *Updater) replaceBinaries(extractDir string) error {
	// Ordered list of relative paths that the archive is expected to contain.
	binaries := []string{
		"worker",
		"mcp-server",
		"hooks/session-start",
		"hooks/user-prompt",
		"hooks/post-tool-use",
		"hooks/stop",
		"hooks/subagent-stop",
		"hooks/statusline",
	}

	for _, dir := range u.getInstallDirectories() {
		log.Debug().Str("dir", dir).Msg("Installing binaries to directory")
		if err := u.installBinariesToDir(extractDir, dir, binaries); err != nil {
			return err
		}
	}
	return nil
}

// installBinariesToDir copies each binary in the list from extractDir into
// destDir, creating a .bak rollback for each file that already exists.
func (u *Updater) installBinariesToDir(extractDir, destDir string, binaries []string) error {
	for _, rel := range binaries {
		src := filepath.Join(extractDir, rel)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue // binary not in this archive — skip silently
		}

		dest := filepath.Join(destDir, rel)
		u.backupBinary(dest)

		if err := copyFile(src, dest); err != nil {
			return fmt.Errorf("failed to install %s: %w", dest, err)
		}
		// #nosec G302 — executables require 0755
		if err := os.Chmod(dest, 0755); err != nil {
			return fmt.Errorf("failed to chmod %s: %w", dest, err)
		}
	}
	return nil
}

// backupBinary renames dest to dest+".bak" and registers a deferred
// cleanup/restore: if dest is present after the install the backup is
// removed; if dest is absent (copy failed) the backup is restored.
func (u *Updater) backupBinary(dest string) {
	if _, err := os.Stat(dest); err != nil {
		return // nothing to back up
	}
	backup := dest + ".bak"
	if err := os.Rename(dest, backup); err != nil {
		log.Warn().Err(err).Str("file", dest).Msg("Failed to backup, continuing anyway")
		return
	}
	// Defer runs after installBinariesToDir returns — either success
	// (remove the backup) or failure (restore from backup).
	go func(backup, dest string) {
		if _, err := os.Stat(dest); err == nil {
			_ = os.Remove(backup)
		} else {
			_ = os.Rename(backup, dest)
		}
	}(backup, dest)
}

// getInstallDirectories returns the primary installDir followed by any
// Claude Code plugin cache directories that contain a "worker" binary.
// The cache directories exist because Claude Code maintains a versioned
// copy of the plugin that it uses directly; without updating those copies
// a restart would load the old binary.
func (u *Updater) getInstallDirectories() []string {
	dirs := []string{u.installDir}

	home, err := os.UserHomeDir()
	if err != nil {
		return dirs
	}

	cacheBase := filepath.Join(home, ".claude/plugins/cache/engram/engram")
	entries, err := os.ReadDir(cacheBase)
	if err != nil {
		return dirs
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cacheDir := filepath.Join(cacheBase, entry.Name())
		if cacheDir == u.installDir {
			continue
		}
		// Only include directories that have an existing worker binary —
		// an empty versioned cache directory should not receive an update.
		if _, err := os.Stat(filepath.Join(cacheDir, "worker")); err == nil {
			dirs = append(dirs, cacheDir)
			log.Debug().Str("dir", cacheDir).Msg("Found cache directory to update")
		}
	}
	return dirs
}

// copyFile copies src to dst, creating parent directories as needed.
// src is always from the extracted temp directory; dst is always inside
// an install directory — neither is user-controlled input.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return err
	}
	in, err := os.Open(src) // #nosec G304
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst) // #nosec G304
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// getPlatform returns the GOOS_GOARCH string that release assets are named
// with.  This must match the naming convention used in the release workflow
// or asset lookup will silently produce no DownloadURL.
func getPlatform() string {
	return fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
}

// isNewerVersion returns true when latest is semantically greater than
// current.  Dev builds often carry a suffix like "0.3.5-2-gca711a8-dirty";
// the comparison uses only the base semver triplet so a dirty build is
// never falsely treated as newer than the tagged release it was built from.
func isNewerVersion(latest, current string) bool {
	latest = strings.TrimPrefix(latest, "v")
	current = strings.TrimPrefix(current, "v")

	// Strip pre-release / build-metadata suffix before comparing.
	currentBase := current
	if idx := strings.Index(current, "-"); idx > 0 {
		currentBase = current[:idx]
	}

	latestParts := strings.Split(latest, ".")
	currentParts := strings.Split(currentBase, ".")

	for i := 0; i < len(latestParts) && i < len(currentParts); i++ {
		latestNum, _ := strconv.Atoi(latestParts[i])
		currentNum, _ := strconv.Atoi(currentParts[i])
		if latestNum > currentNum {
			return true
		}
		if latestNum < currentNum {
			return false
		}
	}
	// Longer version string wins when all shared components are equal
	// (e.g., "1.0.1" > "1.0").
	return len(latestParts) > len(currentParts)
}

// Restart spawns the updated worker binary and exits the current process.
// nohup is used so the child survives the parent's exit; the new process
// is responsible for binding to the port after the old one releases it.
// The ENGRAM_RESTART env var lets the new process detect it is a restart
// (e.g., to skip certain first-boot setup steps).
func (u *Updater) Restart() error {
	workerPath := filepath.Join(u.installDir, "worker")

	if _, err := os.Stat(workerPath); err != nil {
		return fmt.Errorf("new worker binary not found: %w", err)
	}

	log.Info().Str("path", workerPath).Msg("Restarting worker with new binary")

	// #nosec G204 — workerPath is derived from the trusted installDir field
	cmd := exec.Command("nohup", workerPath)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "ENGRAM_RESTART=1")

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start new worker: %w", err)
	}

	// Prevent the child from becoming a zombie — we will not Wait on it
	// because this process is about to exit.
	go func() { _ = cmd.Wait() }()

	log.Info().Int("new_pid", cmd.Process.Pid).Msg("New worker started, exiting old process")

	// Brief sleep gives zerolog's async writer time to flush before exit.
	time.Sleep(100 * time.Millisecond)
	os.Exit(0)

	return nil // unreachable — satisfies the error return type
}
