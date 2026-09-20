$ErrorActionPreference = 'SilentlyContinue'
$found = $false
try {
  $fs = [System.IO.File]::Open('\\\\.\pipe\mini_code_desktop', [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read)
  $fs.Close()
  $found = $true
} catch {}
Write-Output ("pipe_listening=" + $found)
Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*mini_code*' -and $_.Name -notlike '*powershell*' -and $_.Name -notlike '*cmd*' } | ForEach-Object {
  Write-Output ("proc: pid=" + $_.ProcessId + " name=" + $_.Name + " cmd=" + $_.CommandLine)
}
