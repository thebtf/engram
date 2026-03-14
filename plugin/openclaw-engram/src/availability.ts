/**
 * Passive availability detection with 3-strike circuit breaker.
 *
 * The tracker never polls engram — it only records outcomes of calls
 * that the client already makes. After 3 consecutive failures the server
 * is marked unavailable for a 60-second cooldown period, after which the
 * next call is allowed through as a probe.
 */

const STRIKE_THRESHOLD = 3;
const COOLDOWN_MS = 60_000;

export class AvailabilityTracker {
  private consecutiveFailures = 0;
  private unavailableSince: number | null = null;

  /**
   * Record a successful call. Resets the failure counter and restores
   * availability if the server was in cooldown.
   */
  recordSuccess(): void {
    const wasUnavailable = this.unavailableSince !== null;
    this.consecutiveFailures = 0;
    this.unavailableSince = null;
    if (wasUnavailable) {
      console.warn('[engram] server is back online');
    }
  }

  /**
   * Record a failed call. After STRIKE_THRESHOLD consecutive failures
   * the server enters a cooldown period.
   */
  recordFailure(): void {
    this.consecutiveFailures += 1;
    if (this.consecutiveFailures >= STRIKE_THRESHOLD) {
      this.unavailableSince = Date.now();
      console.warn(
        `[engram] server marked unavailable after ${STRIKE_THRESHOLD} consecutive failures — ` +
          `cooldown for ${COOLDOWN_MS / 1000}s`,
      );
    }
  }

  /**
   * Returns true if the server is considered available.
   *
   * After the cooldown period expires the server is tentatively considered
   * available again so that the next call can act as a probe. If that call
   * succeeds, `recordSuccess()` completes the recovery. If it fails,
   * `recordFailure()` restarts the cooldown immediately.
   */
  isAvailable(): boolean {
    if (this.unavailableSince === null) return true;
    const elapsed = Date.now() - this.unavailableSince;
    if (elapsed >= COOLDOWN_MS) {
      // Cooldown expired — reset to allow one probe
      this.consecutiveFailures = 0;
      this.unavailableSince = null;
      return true;
    }
    return false;
  }

  /** Remaining cooldown in milliseconds, or 0 if available. */
  remainingCooldownMs(): number {
    if (this.unavailableSince === null) return 0;
    const elapsed = Date.now() - this.unavailableSince;
    return Math.max(0, COOLDOWN_MS - elapsed);
  }
}
