#!/usr/bin/env node

import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'

function matchesConstraint(values, actual) {
  if (!Array.isArray(values) || values.length === 0) return true
  const excluded = values.filter((value) => value.startsWith('!')).map((value) => value.slice(1))
  if (excluded.includes(actual)) return false
  const included = values.filter((value) => !value.startsWith('!'))
  return included.length === 0 || included.includes(actual)
}

function matchesLinuxLibc(packageKey, libc) {
  const leaf = packageKey.split('/').at(-1) || ''
  if (/(?:^|[-_])musl(?:eabihf)?(?:$|[-_])/.test(leaf)) return libc === 'musl'
  if (/(?:^|[-_])(?:gnu(?:eabihf)?|glibc)(?:$|[-_])/.test(leaf)) return libc === 'glibc'
  return true
}

function currentLibc(platform) {
  if (platform !== 'linux') return null
  const header = process.report?.getReport?.().header
  return header?.glibcVersionRuntime ? 'glibc' : 'musl'
}

function applicableNativePackages(lock, environment) {
  const packages = lock?.packages
  if (!packages || typeof packages !== 'object') throw new Error('package lock has no packages map')

  return Object.entries(packages)
    .filter(([packageKey, metadata]) => {
      if (!packageKey.startsWith('node_modules/') || metadata?.optional !== true) return false
      const hasPlatformConstraint = Array.isArray(metadata.os) && metadata.os.length > 0
      const hasArchitectureConstraint = Array.isArray(metadata.cpu) && metadata.cpu.length > 0
      if (!hasPlatformConstraint && !hasArchitectureConstraint) return false
      if (!matchesConstraint(metadata.os, environment.platform)) return false
      if (!matchesConstraint(metadata.cpu, environment.arch)) return false
      return environment.platform !== 'linux' || matchesLinuxLibc(packageKey, environment.libc)
    })
    .map(([packageKey, metadata]) => {
      if (typeof metadata.version !== 'string' || metadata.version.length === 0) {
        throw new Error(`applicable optional package has no exact version: ${packageKey}`)
      }
      return { packageKey, version: metadata.version }
    })
    .sort((left, right) => left.packageKey.localeCompare(right.packageKey))
}

function verifyInstalledTree({ lock, root, platform, arch, libc }) {
  const resolvedRoot = path.resolve(root)
  const expected = applicableNativePackages(lock, { platform, arch, libc })
  if (expected.length === 0) {
    throw new Error(`no native optional packages apply to platform=${platform} arch=${arch} libc=${libc ?? 'n/a'}`)
  }

  const failures = []
  for (const entry of expected) {
    const packageDirectory = path.resolve(resolvedRoot, entry.packageKey)
    if (!packageDirectory.startsWith(`${resolvedRoot}${path.sep}`)) {
      failures.push(`${entry.packageKey}: unsafe lock path`)
      continue
    }
    const manifestPath = path.join(packageDirectory, 'package.json')
    if (!fs.existsSync(manifestPath)) {
      failures.push(`${entry.packageKey}: missing (expected ${entry.version})`)
      continue
    }
    let installed
    try {
      installed = JSON.parse(fs.readFileSync(manifestPath, 'utf8')).version
    } catch (error) {
      failures.push(`${entry.packageKey}: invalid package.json (${error.message})`)
      continue
    }
    if (installed !== entry.version) {
      failures.push(`${entry.packageKey}: version ${installed ?? '<missing>'}, expected ${entry.version}`)
    }
  }

  if (failures.length > 0) {
    throw new Error(`native optional dependency verification failed:\n- ${failures.join('\n- ')}`)
  }
  return expected
}

function writeInstalledPackage(root, packageKey, version) {
  const directory = path.join(root, packageKey)
  fs.mkdirSync(directory, { recursive: true })
  fs.writeFileSync(path.join(directory, 'package.json'), `${JSON.stringify({ version })}\n`, 'utf8')
}

function runSelfTest() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'engram-native-optional-'))
  const lock = {
    lockfileVersion: 3,
    packages: {
      '': { name: 'fixture' },
      'node_modules/native-linux-x64-gnu': { version: '1.2.3', optional: true, os: ['linux'], cpu: ['x64'] },
      'node_modules/native-linux-x64-musl': { version: '1.2.3', optional: true, os: ['linux'], cpu: ['x64'] },
      'node_modules/native-linux-arm-gnueabihf': { version: '1.2.3', optional: true, os: ['linux'], cpu: ['arm'] },
      'node_modules/native-linux-arm-musleabihf': { version: '1.2.3', optional: true, os: ['linux'], cpu: ['arm'] },
      'node_modules/native-win32-x64-msvc': { version: '1.2.3', optional: true, os: ['win32'], cpu: ['x64'] },
      'node_modules/native-linux-arm64-gnu': { version: '1.2.3', optional: true, os: ['linux'], cpu: ['arm64'] },
      'node_modules/native-darwin-any': { version: '4.5.6', optional: true, os: ['darwin'] },
      'node_modules/native-any-x64': { version: '7.8.9', optional: true, cpu: ['x64'] },
      'node_modules/generic-optional': { version: '9.9.9', optional: true },
    },
  }

  try {
    writeInstalledPackage(root, 'node_modules/native-linux-x64-gnu', '1.2.3')
    writeInstalledPackage(root, 'node_modules/native-any-x64', '7.8.9')
    let checked = verifyInstalledTree({ lock, root, platform: 'linux', arch: 'x64', libc: 'glibc' })
    if (checked.length !== 2 || !checked.some((entry) => entry.packageKey === 'node_modules/native-linux-x64-gnu') || !checked.some((entry) => entry.packageKey === 'node_modules/native-any-x64')) {
      throw new Error(`glibc selection is not exact: ${JSON.stringify(checked)}`)
    }

    fs.rmSync(path.join(root, 'node_modules/native-linux-x64-gnu'), { recursive: true, force: true })
    let missingRejected = false
    try {
      verifyInstalledTree({ lock, root, platform: 'linux', arch: 'x64', libc: 'glibc' })
    } catch (error) {
      missingRejected = error.message.includes('native-linux-x64-gnu: missing')
    }
    if (!missingRejected) throw new Error('missing applicable native package was accepted')

    writeInstalledPackage(root, 'node_modules/native-linux-x64-gnu', '1.2.2')
    let mismatchRejected = false
    try {
      verifyInstalledTree({ lock, root, platform: 'linux', arch: 'x64', libc: 'glibc' })
    } catch (error) {
      mismatchRejected = error.message.includes('version 1.2.2, expected 1.2.3')
    }
    if (!mismatchRejected) throw new Error('wrong native package version was accepted')

    writeInstalledPackage(root, 'node_modules/native-linux-x64-musl', '1.2.3')
    checked = verifyInstalledTree({ lock, root, platform: 'linux', arch: 'x64', libc: 'musl' })
    if (checked.length !== 2 || !checked.some((entry) => entry.packageKey === 'node_modules/native-linux-x64-musl') || !checked.some((entry) => entry.packageKey === 'node_modules/native-any-x64')) {
      throw new Error(`musl selection is not exact: ${JSON.stringify(checked)}`)
    }

    writeInstalledPackage(root, 'node_modules/native-win32-x64-msvc', '1.2.3')
    checked = verifyInstalledTree({ lock, root, platform: 'win32', arch: 'x64', libc: null })
    if (checked.length !== 2 || !checked.some((entry) => entry.packageKey === 'node_modules/native-win32-x64-msvc') || !checked.some((entry) => entry.packageKey === 'node_modules/native-any-x64')) {
      throw new Error(`win32 selection is not exact: ${JSON.stringify(checked)}`)
    }

    writeInstalledPackage(root, 'node_modules/native-darwin-any', '4.5.6')
    checked = verifyInstalledTree({ lock, root, platform: 'darwin', arch: 'x64', libc: null })
    if (checked.length !== 2 || !checked.some((entry) => entry.packageKey === 'node_modules/native-darwin-any') || !checked.some((entry) => entry.packageKey === 'node_modules/native-any-x64')) {
      throw new Error(`partial constraint selection is not exact: ${JSON.stringify(checked)}`)
    }

    writeInstalledPackage(root, 'node_modules/native-linux-arm-gnueabihf', '1.2.3')
    writeInstalledPackage(root, 'node_modules/native-linux-arm-musleabihf', '1.2.3')
    checked = verifyInstalledTree({ lock, root, platform: 'linux', arch: 'arm', libc: 'glibc' })
    if (checked.length !== 1 || checked[0].packageKey !== 'node_modules/native-linux-arm-gnueabihf') {
      throw new Error(`ARM glibc selection is not exact: ${JSON.stringify(checked)}`)
    }

    console.log('native-optional-deps self-test=PASS cases=missing,version,platform,arch,partial-constraints,libc,arm-libc')
  } finally {
    fs.rmSync(root, { recursive: true, force: true })
  }
}

function argumentValue(name) {
  const prefix = `${name}=`
  const argument = process.argv.find((value) => value.startsWith(prefix))
  return argument ? argument.slice(prefix.length) : null
}

if (process.argv.includes('--self-test')) {
  runSelfTest()
} else {
  const lockPath = path.resolve(argumentValue('--lock') || 'package-lock.json')
  const root = path.resolve(argumentValue('--root') || path.dirname(lockPath))
  const platform = argumentValue('--platform') || process.platform
  const arch = argumentValue('--arch') || process.arch
  const libc = argumentValue('--libc') || currentLibc(platform)
  const lock = JSON.parse(fs.readFileSync(lockPath, 'utf8'))
  const checked = verifyInstalledTree({ lock, root, platform, arch, libc })
  console.log(`native-optional-deps verdict=PASS platform=${platform} arch=${arch} libc=${libc ?? 'n/a'} checked=${checked.length}`)
}
