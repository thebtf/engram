using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.IO;
using System.Linq;
using System.Runtime.InteropServices;
using System.Text;

namespace Engram.Recovery
{
    public sealed class FixtureNativeLaunch : IDisposable
    {
        private const uint CreateSuspendedFlag = 0x00000004;
        private const uint CreateUnicodeEnvironment = 0x00000400;
        private const uint ExtendedStartupInfoPresent = 0x00080000;
        private const uint ProcThreadAttributeJobList = 0x0002000D;
        private const uint JobObjectLimitKillOnJobClose = 0x00002000;
        private const uint ProcessTerminate = 0x00000001;
        private const uint ProcessQueryLimitedInformation = 0x00001000;
        private const uint Synchronize = 0x00100000;
        private const uint WaitObject0 = 0x00000000;
        private const uint WaitTimeout = 0x00000102;
        private const uint WaitFailed = 0xFFFFFFFF;
        private const int JobObjectExtendedLimitInformation = 9;
        private const int ErrorAccessDenied = 5;
        private const int ErrorInvalidParameter = 87;
        private const int ErrorInsufficientBuffer = 122;
        private const uint ResumeThreadFailure = 0xFFFFFFFF;
        private IntPtr _job;
        private IntPtr _process;
        private IntPtr _thread;
        private bool _disposed;

        private FixtureNativeLaunch(IntPtr job, IntPtr process, IntPtr thread, int processId, long processStartUtcTicks)
        {
            _job = job;
            _process = process;
            _thread = thread;
            ProcessId = processId;
            ProcessStartUtcTicks = processStartUtcTicks;
        }

        public int ProcessId { get; private set; }
        public long ProcessStartUtcTicks { get; private set; }

        public static FixtureNativeLaunch CreateSuspended(string applicationPath, string workingDirectory, IDictionary<string, string> environment)
        {
            if (!RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
            {
                throw new PlatformNotSupportedException("fixture native launch prerequisite is missing: Windows is required");
            }

            ValidatePath(applicationPath, "staged fixture executable");
            ValidatePath(workingDirectory, "fixture working directory");
            if (!File.Exists(applicationPath))
            {
                throw new FileNotFoundException("staged fixture executable is missing", applicationPath);
            }
            if (environment == null)
            {
                throw new ArgumentNullException("environment");
            }

            IntPtr job = IntPtr.Zero;
            IntPtr attributeList = IntPtr.Zero;
            IntPtr jobList = IntPtr.Zero;
            IntPtr environmentBlock = IntPtr.Zero;
            bool attributesInitialized = false;
            PROCESS_INFORMATION processInformation = new PROCESS_INFORMATION();

            try
            {
                job = CreateJobObjectW(IntPtr.Zero, null);
                if (job == IntPtr.Zero)
                {
                    ThrowLastWin32Error("CreateJobObjectW");
                }
                SetKillOnJobClose(job, true);

                IntPtr attributeListSize = IntPtr.Zero;
                if (InitializeProcThreadAttributeList(IntPtr.Zero, 1, 0, ref attributeListSize))
                {
                    throw new InvalidOperationException("InitializeProcThreadAttributeList unexpectedly accepted an empty attribute list");
                }
                int initializeError = Marshal.GetLastWin32Error();
                if (initializeError != ErrorInsufficientBuffer || attributeListSize == IntPtr.Zero)
                {
                    throw new Win32Exception(initializeError, "InitializeProcThreadAttributeList sizing failed (Win32 error " + initializeError + ")");
                }

                attributeList = Marshal.AllocHGlobal(attributeListSize);
                if (!InitializeProcThreadAttributeList(attributeList, 1, 0, ref attributeListSize))
                {
                    ThrowLastWin32Error("InitializeProcThreadAttributeList");
                }
                attributesInitialized = true;

                jobList = Marshal.AllocHGlobal(IntPtr.Size);
                Marshal.WriteIntPtr(jobList, job);
                if (!UpdateProcThreadAttribute(attributeList, 0, (IntPtr)ProcThreadAttributeJobList, jobList, (IntPtr)IntPtr.Size, IntPtr.Zero, IntPtr.Zero))
                {
                    ThrowLastWin32Error("UpdateProcThreadAttribute(PROC_THREAD_ATTRIBUTE_JOB_LIST)");
                }

                StringBuilder commandLine = BuildExecutableCommandLine(applicationPath);
                environmentBlock = Marshal.StringToHGlobalUni(BuildEnvironmentBlock(environment));
                STARTUPINFOEX startupInfo = new STARTUPINFOEX();
                startupInfo.StartupInfo.cb = (uint)Marshal.SizeOf(typeof(STARTUPINFOEX));
                startupInfo.lpAttributeList = attributeList;
                uint creationFlags = CreateSuspendedFlag | CreateUnicodeEnvironment | ExtendedStartupInfoPresent;
                if (!CreateProcessW(applicationPath, commandLine, IntPtr.Zero, IntPtr.Zero, false, creationFlags, environmentBlock, workingDirectory, ref startupInfo, out processInformation))
                {
                    ThrowLastWin32Error("CreateProcessW");
                }

                long processStartUtcTicks = GetProcessStartUtcTicks(processInformation.hProcess);
                if (processInformation.dwProcessId > Int32.MaxValue)
                {
                    throw new InvalidOperationException("CreateProcessW returned a process identifier outside the supported range");
                }

                FixtureNativeLaunch launch = new FixtureNativeLaunch(job, processInformation.hProcess, processInformation.hThread, (int)processInformation.dwProcessId, processStartUtcTicks);
                job = IntPtr.Zero;
                processInformation.hProcess = IntPtr.Zero;
                processInformation.hThread = IntPtr.Zero;
                return launch;
            }
            finally
            {
                if (attributesInitialized)
                {
                    DeleteProcThreadAttributeList(attributeList);
                }
                if (environmentBlock != IntPtr.Zero)
                {
                    Marshal.FreeHGlobal(environmentBlock);
                }
                if (jobList != IntPtr.Zero)
                {
                    Marshal.FreeHGlobal(jobList);
                }
                if (attributeList != IntPtr.Zero)
                {
                    Marshal.FreeHGlobal(attributeList);
                }
                CloseHandleIfPresent(processInformation.hThread);
                CloseHandleIfPresent(processInformation.hProcess);
                CloseHandleIfPresent(job);
            }
        }

        public static bool TerminateExactProcessAndWait(int processId, long expectedStartUtcTicks, int timeoutMilliseconds)
        {
            if (!RuntimeInformation.IsOSPlatform(OSPlatform.Windows))
            {
                throw new PlatformNotSupportedException("fixture native exact-process termination prerequisite is missing: Windows is required");
            }
            if (processId < 1 || expectedStartUtcTicks < 1 || timeoutMilliseconds < 0)
            {
                throw new ArgumentOutOfRangeException("processId", "fixture exact-process termination arguments are malformed");
            }

            IntPtr process = OpenProcess(ProcessTerminate | ProcessQueryLimitedInformation | Synchronize, false, unchecked((uint)processId));
            if (process == IntPtr.Zero)
            {
                int error = Marshal.GetLastWin32Error();
                if (error == ErrorInvalidParameter)
                {
                    return false;
                }
                throw new Win32Exception(error, "OpenProcess failed (Win32 error " + error + ")");
            }

            try
            {
                long actualStartUtcTicks = GetProcessStartUtcTicks(process);
                if (actualStartUtcTicks != expectedStartUtcTicks)
                {
                    return false;
                }

                uint waitResult = WaitForSingleObject(process, 0);
                if (waitResult == WaitObject0)
                {
                    return false;
                }
                if (waitResult != WaitTimeout)
                {
                    ThrowWaitError(waitResult);
                }

                if (!TerminateProcess(process, 1))
                {
                    int error = Marshal.GetLastWin32Error();
                    if (error == ErrorAccessDenied && WaitForSingleObject(process, 0) == WaitObject0)
                    {
                        return false;
                    }
                    throw new Win32Exception(error, "TerminateProcess failed (Win32 error " + error + ")");
                }

                waitResult = WaitForSingleObject(process, unchecked((uint)timeoutMilliseconds));
                if (waitResult == WaitObject0)
                {
                    return true;
                }
                if (waitResult == WaitTimeout)
                {
                    throw new TimeoutException("fixture exact process did not terminate within " + timeoutMilliseconds + " milliseconds");
                }
                ThrowWaitError(waitResult);
                throw new InvalidOperationException("WaitForSingleObject unexpectedly returned after failure handling");
            }
            finally
            {
                CloseHandleIfPresent(process);
            }
        }

        public void DisableKillOnJobClose()
        {
            ThrowIfDisposed();
            SetKillOnJobClose(_job, false);
        }

        public void Resume()
        {
            ThrowIfDisposed();
            if (ResumeThread(_thread) == ResumeThreadFailure)
            {
                ThrowLastWin32Error("ResumeThread");
            }
        }

        public void Dispose()
        {
            if (_disposed)
            {
                return;
            }
            _disposed = true;
            CloseHandleIfPresent(_job);
            _job = IntPtr.Zero;
            CloseHandleIfPresent(_thread);
            _thread = IntPtr.Zero;
            CloseHandleIfPresent(_process);
            _process = IntPtr.Zero;
        }

        private static StringBuilder BuildExecutableCommandLine(string applicationPath)
        {
            StringBuilder commandLine = new StringBuilder(applicationPath.Length + 2);
            commandLine.Append('"');
            int backslashCount = 0;
            foreach (char character in applicationPath)
            {
                if (character == '\\')
                {
                    backslashCount++;
                }
                else
                {
                    if (character == '"')
                    {
                        commandLine.Append('\\', (backslashCount * 2) + 1);
                    }
                    else
                    {
                        commandLine.Append('\\', backslashCount);
                    }
                    commandLine.Append(character);
                    backslashCount = 0;
                }
            }
            commandLine.Append('\\', backslashCount * 2);
            commandLine.Append('"');
            return commandLine;
        }

        private static void ValidatePath(string path, string label)
        {
            if (String.IsNullOrWhiteSpace(path) || !Path.IsPathRooted(path) || !String.Equals(path, Path.GetFullPath(path), StringComparison.Ordinal))
            {
                throw new ArgumentException(label + " must be an exact absolute path", "path");
            }
        }

        private static string BuildEnvironmentBlock(IDictionary<string, string> environment)
        {
            List<KeyValuePair<string, string>> entries = environment.ToList();
            foreach (KeyValuePair<string, string> entry in entries)
            {
                if (String.IsNullOrWhiteSpace(entry.Key) || entry.Key.IndexOf('\0') >= 0 || entry.Key.IndexOf('=') >= 0 || entry.Value == null || entry.Value.IndexOf('\0') >= 0)
                {
                    throw new ArgumentException("fixture environment contains an invalid name or value", "environment");
                }
            }
            return String.Join("\0", entries.OrderBy(entry => entry.Key, StringComparer.OrdinalIgnoreCase).Select(entry => entry.Key + "=" + entry.Value)) + "\0\0";
        }

        private static long GetProcessStartUtcTicks(IntPtr process)
        {
            FILETIME creation;
            FILETIME exit;
            FILETIME kernel;
            FILETIME user;
            if (!GetProcessTimes(process, out creation, out exit, out kernel, out user))
            {
                ThrowLastWin32Error("GetProcessTimes");
            }
            long fileTime = ((long)creation.dwHighDateTime << 32) | creation.dwLowDateTime;
            return DateTime.FromFileTimeUtc(fileTime).Ticks;
        }

        private static void SetKillOnJobClose(IntPtr job, bool enabled)
        {
            JOBOBJECT_EXTENDED_LIMIT_INFORMATION information = new JOBOBJECT_EXTENDED_LIMIT_INFORMATION();
            information.BasicLimitInformation.LimitFlags = enabled ? JobObjectLimitKillOnJobClose : 0;
            if (!SetInformationJobObject(job, JobObjectExtendedLimitInformation, ref information, (uint)Marshal.SizeOf(typeof(JOBOBJECT_EXTENDED_LIMIT_INFORMATION))))
            {
                ThrowLastWin32Error("SetInformationJobObject(JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)");
            }
        }

        private static void CloseHandleIfPresent(IntPtr handle)
        {
            if (handle != IntPtr.Zero)
            {
                CloseHandle(handle);
            }
        }

        private void ThrowIfDisposed()
        {
            if (_disposed)
            {
                throw new ObjectDisposedException("FixtureNativeLaunch");
            }
        }

        private static void ThrowLastWin32Error(string operation)
        {
            int error = Marshal.GetLastWin32Error();
            throw new Win32Exception(error, operation + " failed (Win32 error " + error + ")");
        }

        private static void ThrowWaitError(uint waitResult)
        {
            if (waitResult == WaitFailed)
            {
                ThrowLastWin32Error("WaitForSingleObject");
            }
            throw new InvalidOperationException("WaitForSingleObject returned an unexpected status " + waitResult);
        }

        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        private struct STARTUPINFO
        {
            public uint cb;
            public string lpReserved;
            public string lpDesktop;
            public string lpTitle;
            public uint dwX;
            public uint dwY;
            public uint dwXSize;
            public uint dwYSize;
            public uint dwXCountChars;
            public uint dwYCountChars;
            public uint dwFillAttribute;
            public uint dwFlags;
            public ushort wShowWindow;
            public ushort cbReserved2;
            public IntPtr lpReserved2;
            public IntPtr hStdInput;
            public IntPtr hStdOutput;
            public IntPtr hStdError;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct STARTUPINFOEX
        {
            public STARTUPINFO StartupInfo;
            public IntPtr lpAttributeList;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct PROCESS_INFORMATION
        {
            public IntPtr hProcess;
            public IntPtr hThread;
            public uint dwProcessId;
            public uint dwThreadId;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct FILETIME
        {
            public uint dwLowDateTime;
            public uint dwHighDateTime;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct JOBOBJECT_BASIC_LIMIT_INFORMATION
        {
            public long PerProcessUserTimeLimit;
            public long PerJobUserTimeLimit;
            public uint LimitFlags;
            public UIntPtr MinimumWorkingSetSize;
            public UIntPtr MaximumWorkingSetSize;
            public uint ActiveProcessLimit;
            public IntPtr Affinity;
            public uint PriorityClass;
            public uint SchedulingClass;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct IO_COUNTERS
        {
            public ulong ReadOperationCount;
            public ulong WriteOperationCount;
            public ulong OtherOperationCount;
            public ulong ReadTransferCount;
            public ulong WriteTransferCount;
            public ulong OtherTransferCount;
        }

        [StructLayout(LayoutKind.Sequential)]
        private struct JOBOBJECT_EXTENDED_LIMIT_INFORMATION
        {
            public JOBOBJECT_BASIC_LIMIT_INFORMATION BasicLimitInformation;
            public IO_COUNTERS IoInfo;
            public UIntPtr ProcessMemoryLimit;
            public UIntPtr JobMemoryLimit;
            public UIntPtr PeakProcessMemoryUsed;
            public UIntPtr PeakJobMemoryUsed;
        }

        [DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
        private static extern IntPtr CreateJobObjectW(IntPtr jobAttributes, string name);

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool SetInformationJobObject(IntPtr job, int jobObjectInformationClass, ref JOBOBJECT_EXTENDED_LIMIT_INFORMATION jobObjectInformation, uint jobObjectInformationLength);

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool InitializeProcThreadAttributeList(IntPtr attributeList, int attributeCount, int flags, ref IntPtr size);

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool UpdateProcThreadAttribute(IntPtr attributeList, uint flags, IntPtr attribute, IntPtr value, IntPtr size, IntPtr previousValue, IntPtr returnSize);

        [DllImport("kernel32.dll")]
        private static extern void DeleteProcThreadAttributeList(IntPtr attributeList);

        [DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool CreateProcessW(string applicationName, [In, Out] StringBuilder commandLine, IntPtr processAttributes, IntPtr threadAttributes, [MarshalAs(UnmanagedType.Bool)] bool inheritHandles, uint creationFlags, IntPtr environment, string currentDirectory, ref STARTUPINFOEX startupInfo, out PROCESS_INFORMATION processInformation);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern uint ResumeThread(IntPtr thread);

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool GetProcessTimes(IntPtr process, out FILETIME creationTime, out FILETIME exitTime, out FILETIME kernelTime, out FILETIME userTime);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern IntPtr OpenProcess(uint processAccess, [MarshalAs(UnmanagedType.Bool)] bool inheritHandle, uint processId);

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool TerminateProcess(IntPtr process, uint exitCode);

        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern uint WaitForSingleObject(IntPtr handle, uint milliseconds);

        [DllImport("kernel32.dll", SetLastError = true)]
        [return: MarshalAs(UnmanagedType.Bool)]
        private static extern bool CloseHandle(IntPtr handle);
    }
}
