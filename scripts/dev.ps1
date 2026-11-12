#Requires -Version 5.1
<#
.SYNOPSIS
    构建并启动 Windows 本机 Pixiv Curator。
.DESCRIPTION
    默认 http://127.0.0.1:5722，私密数据位于 data/dev。
    已有 OAuth 配置时不运行 Python/pip。首次缺配置时准备锁定 gppt 环境。
    管理令牌只保存到私密文件，在管理页输入 Refresh Token 添加账号。
.PARAMETER NoLoginHelper
    不准备 OAuth 应用配置，适合离线检查；仍使用已存在的配置。
.PARAMETER StopAfterReady
    服务就绪后立即清理并退出，用于启动诊断。
#>
[CmdletBinding()]
param(
    [string]$Address = $env:PIXIV_CURATOR_ADDR,
    [string]$DevDir = $env:PIXIV_CURATOR_DEV_DIR,
    [string]$OAuthConfigFile = $env:PIXIV_CURATOR_OAUTH_CONFIG_FILE,
    [string]$Python,
    [switch]$NoBrowser,
    [switch]$NoLoginHelper,
    [switch]$StopAfterReady
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw '请在 Windows PowerShell 5.1 或 PowerShell 7 中运行此脚本。' }
$repo = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($Address)) { $Address = '127.0.0.1:5722' }
if ($Address -notmatch '^(?<host>\[[^\]]+\]|[^:\s]*):(?<port>[0-9]+)$') {
    throw 'Address 必须为 host:port，例如 127.0.0.1:5722 或 [::1]:5722。'
}
$bindHost = $Matches['host'].Trim('[', ']')
$port = 0
if (-not [int]::TryParse($Matches['port'], [ref]$port) -or $port -lt 1 -or $port -gt 65535) { throw '监听端口必须为 1–65535。' }
if ($bindHost -eq '') { $bindHost = '0.0.0.0' }
# Check active listeners, then use the same no-reuse socket policy as Go on Windows.
$activeListeners = [Net.NetworkInformation.IPGlobalProperties]::GetIPGlobalProperties().GetActiveTcpListeners()
foreach ($ip in [Net.Dns]::GetHostAddresses($bindHost)) {
    foreach ($active in $activeListeners) {
        $wildcards = @([Net.IPAddress]::Any, [Net.IPAddress]::IPv6Any)
        if ($active.Port -eq $port -and ($active.Address.Equals($ip) -or
            $active.Address -in $wildcards -or $ip -in $wildcards)) {
            throw "监听地址 $Address 已被占用；未停止任何现有进程。"
        }
    }
    $listener = [Net.Sockets.TcpListener]::new($ip, $port)
    try { $listener.ExclusiveAddressUse = $false; $listener.Start() }
    catch { throw "监听地址 $Address 已被占用或无法绑定；未停止任何现有进程。" }
    finally { $listener.Stop() }
}

function Resolve-RepoPath([string]$Value) {
    if (-not [IO.Path]::IsPathRooted($Value)) { $Value = Join-Path $repo $Value }
    return [IO.Path]::GetFullPath($Value)
}
if ([string]::IsNullOrWhiteSpace($DevDir)) { $DevDir = 'data/dev' }
$DevDir = Resolve-RepoPath $DevDir
if ([string]::IsNullOrWhiteSpace($OAuthConfigFile)) { $OAuthConfigFile = Join-Path $DevDir 'oauth.json' }
$OAuthConfigFile = Resolve-RepoPath $OAuthConfigFile
$tokenFile = Join-Path $DevDir 'admin-token'
$refreshFile = Join-Path $DevDir 'pixiv-refresh-token'
$database = Join-Path $DevDir 'curator.db'
$probeHost = $bindHost
if ($probeHost -eq '0.0.0.0') { $probeHost = '127.0.0.1' }
if ($probeHost -eq '::') { $probeHost = '::1' }
if ($probeHost.Contains(':')) { $probeHost = "[$probeHost]" }
$localBase = "http://$($probeHost):$port"
$publicBase = $env:PIXIV_CURATOR_PUBLIC_BASE_URL
if ([string]::IsNullOrWhiteSpace($publicBase)) { $publicBase = $localBase }
$publicUri = $null
if (-not [uri]::TryCreate($publicBase, [UriKind]::Absolute, [ref]$publicUri) -or
    $publicUri.Scheme -notin @('http', 'https') -or $publicUri.UserInfo -ne '' -or
    $publicUri.AbsolutePath -ne '/' -or $publicUri.Query -ne '' -or $publicUri.Fragment -ne '') {
    throw 'PIXIV_CURATOR_PUBLIC_BASE_URL 必须为不含路径或凭据的 HTTP(S) 来源地址。'
}
function Assert-RegularPath([string]$Path, [bool]$Directory) {
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or $item.PSIsContainer -ne $Directory) {
        throw "路径必须为普通文件或目录，不能是符号链接/联接：$Path"
    }
}
function Protect-PrivatePath([string]$Path, [bool]$Directory) {
    Assert-RegularPath $Path $Directory
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $grant = "*$($sid):F"
    if ($Directory) { $grant = "*$($sid):(OI)(CI)F" }
    # icacls changes only the DACL, without requesting SeSecurityPrivilege for SACLs.
    & "$env:SystemRoot\System32\icacls.exe" $Path '/inheritance:r' '/grant:r' $grant '/Q' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "无法设置私密访问权限：$Path" }
    $item = Get-Item -LiteralPath $Path -Force
    if ($PSVersionTable.PSVersion.Major -ge 6) { $acl = [IO.FileSystemAclExtensions]::GetAccessControl($item) }
    else { $acl = $item.GetAccessControl() }
    $others = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]) |
        Where-Object { $_.AccessControlType -eq 'Allow' -and
            $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -ne $sid } |
        ForEach-Object { '*' + $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value } |
        Select-Object -Unique)
    if ($others.Count -gt 0) {
        & "$env:SystemRoot\System32\icacls.exe" $Path '/remove:g' $others '/Q' | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "无法移除多余访问权限：$Path" }
    }
}
function Quote-NativeArgument([string]$Value) {
    $escaped = [regex]::Replace($Value, '(\\*)"', {
        param($match)
        $slashes = $match.Groups[1].Value
        return $slashes + $slashes + '\"'
    })
    return '"' + ($escaped -replace '(\\+)$', '$1$1') + '"'
}
# Job closure terminates all launched descendants, including on console closure.
if (-not ('PixivCurator.LaunchJob' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Threading;
using System.IO;
namespace PixivCurator {
    public sealed class LaunchJob : IDisposable {
        [StructLayout(LayoutKind.Sequential)] struct BasicLimits {
            public long ProcessTime, JobTime; public uint Flags;
            public UIntPtr MinWorkingSet, MaxWorkingSet; public uint ActiveProcesses;
            public UIntPtr Affinity; public uint Priority, Scheduling;
        }
        [StructLayout(LayoutKind.Sequential)] struct IoCounters {
            public ulong ReadOps, WriteOps, OtherOps, ReadBytes, WriteBytes, OtherBytes;
        }
        [StructLayout(LayoutKind.Sequential)] struct ExtendedLimits {
            public BasicLimits Basic; public IoCounters Io;
            public UIntPtr ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory;
        }
        [DllImport("kernel32.dll", SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr attributes, string name);
        [DllImport("kernel32.dll", SetLastError=true)] static extern bool SetInformationJobObject(IntPtr job, int infoClass, IntPtr info, uint length);
        [DllImport("kernel32.dll", SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);
        [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
        delegate bool ControlHandler(uint control);
        [DllImport("kernel32.dll", SetLastError=true)] static extern bool SetConsoleCtrlHandler(ControlHandler handler, bool add);
        IntPtr handle;
        ControlHandler controlHandler;
        int stopped;
        public bool StopRequested { get { return Volatile.Read(ref stopped)!=0; } }
        bool HandleControl(uint control) {
            if(control==0 || control==1) { Interlocked.Exchange(ref stopped,1); return true; }
            if(control==2 || control==5 || control==6) Dispose();
            return false;
        }
        public LaunchJob() {
            handle=CreateJobObject(IntPtr.Zero, null);
            if(handle==IntPtr.Zero) throw new Win32Exception();
            var limits=new ExtendedLimits(); limits.Basic.Flags=0x2000;
            int size=Marshal.SizeOf(typeof(ExtendedLimits)); IntPtr memory=Marshal.AllocHGlobal(size);
            try {
                Marshal.StructureToPtr(limits,memory,false);
                if(!SetInformationJobObject(handle,9,memory,(uint)size)) { Dispose(); throw new Win32Exception(); }
            } finally { Marshal.FreeHGlobal(memory); }
            controlHandler=HandleControl;
            if(!SetConsoleCtrlHandler(controlHandler,true)) {
                int error=Marshal.GetLastWin32Error(); controlHandler=null;
                if(error!=6) { Dispose(); throw new Win32Exception(error); }
            }
        }
        public static void ForwardOutput(Process process) {
            process.OutputDataReceived += delegate(object sender, DataReceivedEventArgs args) {
                if(args.Data!=null) try { Console.Out.WriteLine(args.Data); } catch(IOException) {} catch(ObjectDisposedException) {}
            };
            process.ErrorDataReceived += delegate(object sender, DataReceivedEventArgs args) {
                if(args.Data!=null) try { Console.Error.WriteLine(args.Data); } catch(IOException) {} catch(ObjectDisposedException) {}
            };
            process.BeginOutputReadLine(); process.BeginErrorReadLine();
        }
        public void Attach(Process process) {
            if(!AssignProcessToJobObject(handle,process.Handle) && !process.HasExited) throw new Win32Exception();
        }
        public void Dispose() {
            if(controlHandler!=null) { SetConsoleCtrlHandler(controlHandler,false); controlHandler=null; }
            IntPtr current=Interlocked.Exchange(ref handle,IntPtr.Zero);
            if(current!=IntPtr.Zero) CloseHandle(current);
        }
    }
}
'@
}
$job = [PixivCurator.LaunchJob]::new()
$service = $null
$http = $null
function Stop-Child([Diagnostics.Process]$Child) {
    if ($null -ne $Child) {
        try { if (-not $Child.HasExited) { $Child.Kill(); [void]$Child.WaitForExit(10000) } }
        finally { $Child.Dispose() }
    }
}
function Start-Child([string]$File, [string[]]$Arguments, [hashtable]$Environment = @{}) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $File
    $info.WorkingDirectory = $repo
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.Arguments = ($Arguments | ForEach-Object { Quote-NativeArgument $_ }) -join ' '
    foreach ($key in $Environment.Keys) {
        if ($null -eq $Environment[$key]) { $info.EnvironmentVariables.Remove($key) }
        else { $info.EnvironmentVariables[$key] = [string]$Environment[$key] }
    }
    $child = [Diagnostics.Process]::Start($info)
    try { $job.Attach($child); [PixivCurator.LaunchJob]::ForwardOutput($child) } catch { Stop-Child $child; throw }
    return $child
}
function Invoke-Checked([string]$File, [string[]]$Arguments, [hashtable]$Environment = @{}) {
    $child = Start-Child $File $Arguments $Environment
    try {
        while (-not $child.WaitForExit(200)) {
            if ($job.StopRequested) { throw '启动已取消。' }
        }
        $child.WaitForExit()
        if ($child.ExitCode -ne 0) { throw "启动准备命令失败（退出码 $($child.ExitCode)）。" }
    } finally { Stop-Child $child }
}
try {
    if (-not (Test-Path -LiteralPath $DevDir)) { [void][IO.Directory]::CreateDirectory($DevDir) }
    Protect-PrivatePath $DevDir $true
    foreach ($directory in @((Join-Path $DevDir 'private'), (Join-Path $DevDir 'private/accounts'))) {
        if (Test-Path -LiteralPath $directory) { Protect-PrivatePath $directory $true }
    }
    if (Test-Path -LiteralPath $tokenFile) {
        Protect-PrivatePath $tokenFile $false
        if ((Get-Item -LiteralPath $tokenFile).Length -gt 16384) { throw '管理 token 文件过大。' }
    }
    if (-not (Test-Path -LiteralPath $tokenFile) -or (Get-Item -LiteralPath $tokenFile).Length -eq 0) {
        $bytes = [byte[]]::new(32)
        $random = [Security.Cryptography.RandomNumberGenerator]::Create()
        try { $random.GetBytes($bytes) } finally { $random.Dispose() }
        $token = [Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
        $mode = [IO.FileMode]::CreateNew
        if (Test-Path -LiteralPath $tokenFile) { $mode = [IO.FileMode]::Truncate }
        $stream = [IO.File]::Open($tokenFile, $mode, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try {
            $encoded = [Text.Encoding]::ASCII.GetBytes($token + [Environment]::NewLine)
            $stream.Write($encoded, 0, $encoded.Length)
            $stream.Flush($true)
        } finally { $stream.Dispose() }
        $token = $null
        Protect-PrivatePath $tokenFile $false
    }
    if (Test-Path -LiteralPath $refreshFile) { Protect-PrivatePath $refreshFile $false }
    if (Test-Path -LiteralPath $OAuthConfigFile) {
        Protect-PrivatePath $OAuthConfigFile $false
        if ((Get-Item -LiteralPath $OAuthConfigFile).Length -eq 0) { throw '已有 OAuth 配置为空；请修复该文件，脚本不会覆写。' }
    } elseif (-not $NoLoginHelper) {
        $requirements = Join-Path $PSScriptRoot 'requirements-login.txt'
        $helper = Join-Path $PSScriptRoot 'pixiv-login.py'
        if (-not (Test-Path -LiteralPath $requirements) -or -not (Test-Path -LiteralPath $helper)) { throw '缺少 OAuth 配置辅助文件。' }
        $pythonArgs = @()
        if ($Python) { $pythonCommand = Get-Command $Python -CommandType Application -ErrorAction Stop }
        else {
            $pythonCommand = Get-Command py.exe -CommandType Application -ErrorAction SilentlyContinue
            if ($pythonCommand) { $pythonArgs = @('-3') }
            else {
                $pythonCommand = Get-Command python.exe -CommandType Application -ErrorAction SilentlyContinue |
                    Where-Object { $_.Source -notlike '*\WindowsApps\*' } | Select-Object -First 1
            }
        }
        if (-not $pythonCommand) { throw '首次准备 OAuth 配置需要 Python 3；可用 -Python 指定解释器，或配置已有的 OAuth 文件。' }
        $venv = Join-Path $DevDir 'venv-windows'
        $venvPython = Join-Path $venv 'Scripts/python.exe'
        if (-not (Test-Path -LiteralPath $venvPython)) {
            Invoke-Checked $pythonCommand.Source ($pythonArgs + @('-m', 'venv', $venv))
        }
        $hash = (Get-FileHash -LiteralPath $requirements -Algorithm SHA256).Hash
        $marker = Join-Path $venv '.requirements-login.sha256'
        $installedHash = ''
        if (Test-Path -LiteralPath $marker) { $installedHash = [IO.File]::ReadAllText($marker).Trim() }
        if ($installedHash -ne $hash) {
            Invoke-Checked $venvPython @('-m', 'pip', 'install', '--disable-pip-version-check', '-r', $requirements)
        }
        Invoke-Checked $venvPython @('-c', 'import importlib.metadata; assert importlib.metadata.version("gppt") == "5.1.0"')
        Invoke-Checked $venvPython @('-m', 'pip', 'check')
        [IO.File]::WriteAllText($marker, $hash, [Text.Encoding]::ASCII)
        $oauthParent = Split-Path -Parent $OAuthConfigFile
        if (-not (Test-Path -LiteralPath $oauthParent)) { [void][IO.Directory]::CreateDirectory($oauthParent) }
        $oauthTemporary = Join-Path $DevDir ('.oauth-' + [guid]::NewGuid().ToString('N') + '.json')
        try {
            Invoke-Checked $venvPython @($helper, 'configure', $oauthTemporary)
            Protect-PrivatePath $oauthTemporary $false
            [IO.File]::Move($oauthTemporary, $OAuthConfigFile)
            Protect-PrivatePath $OAuthConfigFile $false
        } finally {
            if (Test-Path -LiteralPath $oauthTemporary) { Remove-Item -LiteralPath $oauthTemporary }
        }
    }
    if (-not (Test-Path -LiteralPath $OAuthConfigFile)) { Write-Warning '离线启动：未准备 OAuth 配置，暂不能添加 Pixiv 账号。' }
    if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { $nativeArch = 'arm64' }
    elseif ([Environment]::Is64BitOperatingSystem) { $nativeArch = 'amd64' }
    else { throw '仅支持 Windows amd64/arm64。' }
    $hasGoSource = (Test-Path -LiteralPath (Join-Path $repo 'go.mod') -PathType Leaf) -and
        (Test-Path -LiteralPath (Join-Path $repo 'cmd/api/main.go') -PathType Leaf)
    if ($hasGoSource) {
        $go = Get-Command go.exe -CommandType Application -ErrorAction SilentlyContinue
        if ($null -eq $go) { throw '源码目录启动需要 Go；未找到 Go，请先安装 Go 1.26+ 并加入 PATH。' }
        $binary = Join-Path $repo 'bin/pixiv-curator.exe'
        [void][IO.Directory]::CreateDirectory((Split-Path -Parent $binary))
        Write-Host '正在构建 Pixiv Curator...'
        Invoke-Checked $go.Source @('build', '-trimpath', '-o', $binary, './cmd/api') @{
            GOOS = 'windows'; GOARCH = $nativeArch; CGO_ENABLED = '0'
        }
    } else {
        $binary = Join-Path $repo 'pixiv-curator.exe'
        if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) {
            throw '发布目录缺少根目录 pixiv-curator.exe；请重新解压完整的 Windows 发布包。'
        }
        Write-Host '使用发布目录中的 Pixiv Curator。'
    }
    $environment = @{
        PIXIV_CURATOR_ADDR = $Address; PIXIV_CURATOR_PUBLIC_BASE_URL = $publicBase.TrimEnd('/')
        PIXIV_CURATOR_DATABASE_PATH = $database; PIXIV_CURATOR_ADMIN_TOKEN_FILE = $tokenFile
        PIXIV_CURATOR_PIXIV_TOKEN_FILE = $refreshFile; PIXIV_CURATOR_LOGIN_PYTHON = $null
        PIXIV_CURATOR_LOGIN_HELPER = $null; PIXIV_CURATOR_CALLBACK_DIR = $null
        PIXIV_CURATOR_OAUTH_CONFIG_FILE = $null
    }
    if (Test-Path -LiteralPath $OAuthConfigFile) { $environment.PIXIV_CURATOR_OAUTH_CONFIG_FILE = $OAuthConfigFile }
    $service = Start-Child $binary @() $environment
    Add-Type -AssemblyName System.Net.Http
    $handler = [Net.Http.HttpClientHandler]::new()
    $handler.UseProxy = $false
    $http = [Net.Http.HttpClient]::new($handler)
    $http.Timeout = [TimeSpan]::FromSeconds(1)
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $ready = $false
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($job.StopRequested) { throw '启动已取消。' }
        if ($service.HasExited) { throw "服务在就绪前退出（退出码 $($service.ExitCode)）。" }
        try {
            $response = $http.GetAsync("$localBase/readyz").GetAwaiter().GetResult()
            try { $ready = [int]$response.StatusCode -eq 200 } finally { $response.Dispose() }
        } catch { $ready = $false }
        if ($ready) { break }
        Start-Sleep -Milliseconds 200
    }
    if (-not $ready) { throw '等待本机 /readyz 超时。' }
    Write-Host "服务已就绪：$publicBase"
    Write-Host "登录页：$($publicBase.TrimEnd('/'))/admin/login"
    Write-Host "管理 token 文件：$tokenFile"
    if ($StopAfterReady) { return }
    if (-not $NoBrowser -and $env:PIXIV_CURATOR_OPEN_BROWSER -ne '0') {
        try { Start-Process -FilePath "$($publicBase.TrimEnd('/'))/admin/login" -WindowStyle Hidden }
        catch { Write-Warning '无法自动打开浏览器，请手动访问上面的登录页。' }
    }
    Write-Host '按 Ctrl+C 停止服务。'
    while (-not $service.WaitForExit(200)) {
        if ($job.StopRequested) { return }
    }
    if ($service.ExitCode -ne 0) { throw "服务已退出（退出码 $($service.ExitCode)）。" }
} finally {
    if ($null -ne $http) { $http.Dispose() }
    $job.Dispose()
    Stop-Child $service
}
