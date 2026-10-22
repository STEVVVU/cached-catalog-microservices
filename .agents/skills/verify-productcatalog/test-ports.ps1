$ErrorActionPreference = 'Stop'
$tokens = $null
$errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'verify.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw ($errors -join '; ') }
$function = $ast.Find({param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-GatewayLoopbackPorts'}, $true)
if (-not $function) { throw 'Missing shared gateway-port allocator' }
Invoke-Expression $function.Extent.Text
$ports = Get-GatewayLoopbackPorts
if ($ports.Grpc -le 0 -or $ports.Admin -le 0 -or $ports.Grpc -eq $ports.Admin) { throw 'Gateway endpoints must use distinct assigned ports' }
$grpc = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $ports.Grpc)
$admin = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $ports.Admin)
try {
    $grpc.Start()
    $admin.Start()
    Write-Output 'Gateway port reservations are distinct and released for owned listeners'
} finally {
    $grpc.Stop()
    $admin.Stop()
}
