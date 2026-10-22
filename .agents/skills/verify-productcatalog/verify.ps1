param()

$ErrorActionPreference = 'Stop'
$projectRoot = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path
$serviceRoot = Join-Path $projectRoot 'src\productcatalogservice'
$preferredGo = 'C:\Users\steve\.codex\tools\coursework-go\go\bin\go.exe'
if ($env:P24_GO) {
    $go = $env:P24_GO
} elseif ($IsWindows -and (Test-Path $preferredGo)) {
    $go = $preferredGo
} else {
    $goCommand = Get-Command -Name go -ErrorAction SilentlyContinue
    if (-not $goCommand -and $IsWindows) { $goCommand = Get-Command -Name go.exe -ErrorAction Stop }
    if (-not $goCommand) { throw 'Go 1.27.3 was not found; put it on PATH or set P24_GO' }
    $go = $goCommand.Source
}
$evidenceRoot = Join-Path $projectRoot 'reports\evidence'
$stamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ')
$evidenceDir = Join-Path $evidenceRoot $stamp
$tempRoot = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } elseif ($env:TEMP) { $env:TEMP } else { [System.IO.Path]::GetTempPath() }
$workDir = Join-Path $tempRoot "p24-catalog-$PID-$stamp"
$serverExe = Join-Path $workDir 'productcatalogservice.exe'
$ctlExe = Join-Path $workDir 'catalogctl.exe'
$dbPath = Join-Path $workDir 'catalog.db'
$dbPort = 0
$gatewayPort = 0
$adminPort = 0
$started = [System.Collections.Generic.List[object]]::new()
$proofs = [System.Collections.Generic.List[object]]::new()
$benchmarks = [System.Collections.Generic.List[object]]::new()
$result = 'failed'
$failure = $null

function Assert-That([bool]$condition, [string]$message) {
    if (-not $condition) { throw $message }
}

function Get-FreeLoopbackPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
    return $port
}

function Add-Proof([string]$name, [string]$command, [string]$observed) {
    $proofs.Add([ordered]@{ action = $name; command = $command; observed = $observed })
}

function Start-CatalogProcess([string]$name, [hashtable]$environment) {
    $stdout = Join-Path $evidenceDir "$name.stdout.log"
    $stderr = Join-Path $evidenceDir "$name.stderr.log"
    $startParameters = @{
        FilePath = $serverExe
        WorkingDirectory = $serviceRoot
        PassThru = $true
        RedirectStandardOutput = $stdout
        RedirectStandardError = $stderr
        Environment = $environment
    }
    if ($IsWindows) { $startParameters.WindowStyle = 'Hidden' }
    $process = Start-Process @startParameters
    $entry = [pscustomobject]@{ Name = $name; Process = $process; Stdout = $stdout; Stderr = $stderr }
    $started.Add($entry)
    return $entry
}

function Stop-CatalogProcess($entry) {
    if ($null -ne $entry -and $null -ne $entry.Process) {
        $entry.Process.Refresh()
        if (-not $entry.Process.HasExited) {
            $entry.Process.Kill()
            [void]$entry.Process.WaitForExit(5000)
        }
    }
}

function Start-Database([string]$responseDelay) {
    $environment = @{
        CATALOG_DB_MODE = '1'
        CATALOG_DB_BIND_ADDRESS = '127.0.0.1'
        CATALOG_DB_PORT = "$dbPort"
        CATALOG_DB_PATH = $dbPath
        CATALOG_SEED_PATH = (Join-Path $serviceRoot 'products.json')
        CATALOG_DB_RESPONSE_DELAY = $responseDelay
        DISABLE_PROFILER = '1'
    }
    return Start-CatalogProcess "db-$($started.Count + 1)" $environment
}

function Start-Gateway([string]$mode, [string]$ttl) {
    $script:gatewayPort = Get-FreeLoopbackPort
    $script:adminPort = Get-FreeLoopbackPort
    $environment = @{
        PORT = "$gatewayPort"
        CATALOG_BACKEND_ADDR = "127.0.0.1:$dbPort"
        CATALOG_CACHE_MODE = $mode
        CATALOG_CACHE_TTL = $ttl
        CATALOG_BACKEND_TIMEOUT = '2s'
        CATALOG_ADMIN_PORT = "$adminPort"
        DISABLE_PROFILER = '1'
    }
    return Start-CatalogProcess "gateway-$mode-$($started.Count + 1)" $environment
}

function Invoke-ClientResult([string[]]$arguments, [int]$port = $gatewayPort, [int]$httpPort = $adminPort) {
    $env:CATALOGCTL_GRPC_ADDR = "127.0.0.1:$port"
    $env:CATALOGCTL_ADMIN_URL = "http://127.0.0.1:$httpPort"
    $nativePreference = $PSNativeCommandUseErrorActionPreference
    $PSNativeCommandUseErrorActionPreference = $false
    try {
        $output = @(& $ctlExe @arguments 2>&1)
        $code = $LASTEXITCODE
    } finally {
        $PSNativeCommandUseErrorActionPreference = $nativePreference
    }
    return [pscustomobject]@{ Code = $code; Text = ($output -join "`n") }
}

function Invoke-Client([string[]]$arguments, [int]$port = $gatewayPort, [int]$httpPort = $adminPort) {
    $response = Invoke-ClientResult $arguments $port $httpPort
    if ($response.Code -ne 0) { throw "catalogctl $($arguments -join ' ') failed ($($response.Code)): $($response.Text)" }
    return $response.Text
}

function Wait-Database([int]$port) {
    $until = [DateTime]::UtcNow.AddSeconds(10)
    do {
        $response = Invoke-ClientResult @('list') $port 0
        if ($response.Code -eq 0) { return $response.Text }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $until)
    throw "database gRPC did not become usable: $($response.Text)"
}

function Wait-DatabaseTcp([int]$port) {
    $until = [DateTime]::UtcNow.AddSeconds(10)
    do {
        $client = [System.Net.Sockets.TcpClient]::new()
        try {
            $task = $client.ConnectAsync('127.0.0.1', $port)
            if ($task.Wait(250) -and $client.Connected) { return }
        } catch {
        } finally {
            $client.Dispose()
        }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $until)
    throw "database did not bind TCP port $port"
}

function Wait-Gateway([int]$httpPort) {
    $until = [DateTime]::UtcNow.AddSeconds(10)
    do {
        $response = Invoke-ClientResult @('stats') $gatewayPort $httpPort
        if ($response.Code -eq 0) { return $response.Text | ConvertFrom-Json }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $until)
    throw "gateway admin endpoint did not become usable: $($response.Text)"
}

function Wait-SnapshotCaptured($database, [uint64]$revision, [int]$startingLines) {
    $until = [DateTime]::UtcNow.AddSeconds(10)
    $needle = "catalog snapshot captured revision=$revision;"
    do {
        $lines = @(Get-Content $database.Stdout -ErrorAction SilentlyContinue)
        if ($lines.Count -gt $startingLines -and (($lines | Select-Object -Skip $startingLines) -join "`n").Contains($needle)) { return }
        if ($database.Process.HasExited) { throw "database process exited before capturing revision $revision" }
        Start-Sleep -Milliseconds 25
    } while ([DateTime]::UtcNow -lt $until)
    throw "database did not report snapshot capture for revision $revision"
}

function Get-Metric([string]$text, [string]$name) {
    $match = [regex]::Match($text, "(?m)^$([regex]::Escape($name))\s+(\d+)$")
    if (-not $match.Success) { throw "metric $name was missing" }
    return [uint64]$match.Groups[1].Value
}

function Get-Metrics {
    return Invoke-Client @('metrics')
}

function New-CatalogFile([string]$name, [switch]$duplicateId) {
    $catalog = Get-Content -Raw (Join-Path $serviceRoot 'products.json') | ConvertFrom-Json
    $catalog.products[0].name = $name
    if ($duplicateId) { $catalog.products[1].id = $catalog.products[0].id }
    $path = Join-Path $workDir (([regex]::Replace($name, '[^A-Za-z0-9]+', '-')) + '.json')
    $catalog | ConvertTo-Json -Depth 100 | Set-Content -LiteralPath $path -Encoding utf8NoBOM
    return $path
}

function Update-Catalog([string]$name, [uint64]$expectedRevision) {
    $path = New-CatalogFile $name
    $text = Invoke-Client @('update', $path, "$expectedRevision")
    return ($text | ConvertFrom-Json)
}

function Restart-Gateway([string]$mode, [string]$ttl) {
    Stop-CatalogProcess $script:gateway
    $script:gateway = Start-Gateway $mode $ttl
    [void](Wait-Gateway $adminPort)
}

function Start-BackgroundList([string]$name) {
    $stdout = Join-Path $evidenceDir "$name.stdout.log"
    $stderr = Join-Path $evidenceDir "$name.stderr.log"
    $environment = @{ CATALOGCTL_GRPC_ADDR = "127.0.0.1:$gatewayPort" }
    $startParameters = @{
        FilePath = $ctlExe
        ArgumentList = @('list')
        PassThru = $true
        RedirectStandardOutput = $stdout
        RedirectStandardError = $stderr
        Environment = $environment
    }
    if ($IsWindows) { $startParameters.WindowStyle = 'Hidden' }
    return Start-Process @startParameters
}

try {
    New-Item -ItemType Directory -Force -Path $evidenceDir, $workDir | Out-Null
    $localToolCache = 'C:\Users\steve\.codex\tools\coursework-go'
    if ($IsWindows -and (Test-Path (Join-Path $localToolCache 'gopath'))) {
        $goCacheRoot = $localToolCache
    } else {
        $goCacheRoot = Join-Path $workDir 'go-cache'
        New-Item -ItemType Directory -Force -Path $goCacheRoot | Out-Null
    }
    $env:GOPATH = Join-Path $goCacheRoot 'gopath'
    $env:GOMODCACHE = Join-Path $env:GOPATH 'pkg\mod'
    $env:GOCACHE = Join-Path $goCacheRoot 'cache'
    if ($env:GOFLAGS -notmatch '(^|\s)-modcacherw(\s|$)') {
        $env:GOFLAGS = (@($env:GOFLAGS, '-modcacherw') | Where-Object { $_ }) -join ' '
    }
    $env:PATH = (Join-Path $projectRoot '.tools\bin') + [System.IO.Path]::PathSeparator + $env:PATH
    Assert-That (Test-Path $go) "pinned Go 1.27.3 toolchain was not found at $go"
    $goVersion = & $go version
    Assert-That ($LASTEXITCODE -eq 0 -and $goVersion -match 'go1\.27\.3(?:\s|$)') "Go 1.27.3 is required; found $goVersion"

    Push-Location $serviceRoot
    try {
        & $go test ./... *> (Join-Path $evidenceDir 'go-test.log')
        Assert-That ($LASTEXITCODE -eq 0) "go test ./... failed; see go-test.log"
        & $go build -o $serverExe . *> (Join-Path $evidenceDir 'go-build-server.log')
        Assert-That ($LASTEXITCODE -eq 0) "building the product catalog server failed"
        & $go build -o $ctlExe ./cmd/catalogctl *> (Join-Path $evidenceDir 'go-build-catalogctl.log')
        Assert-That ($LASTEXITCODE -eq 0) "building catalogctl failed"
    } finally { Pop-Location }

    $dbPort = Get-FreeLoopbackPort
    $gatewayPort = Get-FreeLoopbackPort
    $adminPort = Get-FreeLoopbackPort
    $script:database = Start-Database '0s'
    $catalogText = Wait-Database $dbPort
    $catalog = $catalogText | ConvertFrom-Json
    Assert-That ($catalog.products.Count -eq 9) "seed catalog returned $($catalog.products.Count) products, expected 9"
    Add-Proof 'database seed and ListProducts' "catalogctl list (gRPC to 127.0.0.1:$dbPort)" "received $($catalog.products.Count) persisted products from revision 1"

    $script:gateway = Start-Gateway 'none' '2s'
    [void](Wait-Gateway $adminPort)
    $beforeStats = (Invoke-Client @('stats') | ConvertFrom-Json)
    $list = (Invoke-Client @('list') | ConvertFrom-Json)
    $productId = $list.products[0].id
    $firstName = $list.products[0].name
    $product = (Invoke-Client @('get', $productId) | ConvertFrom-Json)
    $search = (Invoke-Client @('search', $firstName) | ConvertFrom-Json)
    Assert-That ($product.id -eq $productId) 'GetProduct returned a different product ID'
    Assert-That ($search.results.Count -ge 1) 'SearchProducts did not find the seeded product'
    $afterStats = (Invoke-Client @('stats') | ConvertFrom-Json)
    Assert-That (($afterStats.backendReads - $beforeStats.backendReads) -eq 3) 'no-cache List/Get/Search did not produce three backend gRPC reads'
    Add-Proof 'existing ProductCatalogService journeys' 'catalogctl list, get, search over gRPC' "List=$($list.products.Count); Get=$($product.name); Search=$($search.results.Count); DB reads delta=$($afterStats.backendReads - $beforeStats.backendReads)"

    $invalidPath = New-CatalogFile 'invalid duplicate IDs' -duplicateId
    $invalidWrite = Invoke-ClientResult @('update', $invalidPath, '1')
    Assert-That ($invalidWrite.Code -ne 0 -and $invalidWrite.Text.Contains('400')) 'duplicate-ID write was not rejected as HTTP 400'
    Assert-That ((Invoke-Client @('revision') | ConvertFrom-Json).revision -eq 1) 'invalid write changed the persistent revision'
    $valid = Update-Catalog 'P24 updated catalog' 1
    Assert-That ($valid.revision -eq 2) 'valid write did not increment revision to 2'
    $updated = (Invoke-Client @('list') | ConvertFrom-Json)
    Assert-That ($updated.products[0].name -eq 'P24 updated catalog') 'ListProducts did not return the committed catalog update'
    Add-Proof 'validated transactional update' "catalogctl update FILE 1; then list/revision" "invalid duplicate IDs rejected without revision change; valid write returned revision $($valid.revision) and same-version read returned $($updated.products[0].name)"

    $databaseStats = (Invoke-Client @('stats') | ConvertFrom-Json)
    Stop-CatalogProcess $script:database
    $startedAt = [DateTime]::UtcNow
    $unavailable = Invoke-ClientResult @('list')
    $failureMs = [math]::Round(([DateTime]::UtcNow - $startedAt).TotalMilliseconds, 1)
    Assert-That ($unavailable.Code -ne 0 -and ($unavailable.Text.Contains('DeadlineExceeded') -or $unavailable.Text.Contains('Unavailable'))) 'unavailable backend did not return an explicit gRPC failure'
    Assert-That ($failureMs -lt 4000) "unavailable backend exceeded the bounded timeout ($failureMs ms)"
    $script:database = Start-Database '0s'
    [void](Wait-Database $dbPort)
    [void](Wait-Gateway $adminPort)
    $persisted = (Invoke-Client @('list') | ConvertFrom-Json)
    Assert-That ($persisted.products[0].name -eq 'P24 updated catalog') 'catalog update did not survive database process restart'
    Assert-That ((Invoke-Client @('revision') | ConvertFrom-Json).revision -eq 2) 'catalog revision did not survive database process restart'
    Add-Proof 'bounded backend failure and database restart' 'stop exact DB process; List; restart DB on same bbolt file; List/revision' "error=$($unavailable.Text); failure=${failureMs}ms; revision=$($databaseStats.revision) and updated product persisted after restart"

    Restart-Gateway 'ttl' '400ms'
    [void](Invoke-Client @('list'))
    $ttlWarm = Get-Metric (Get-Metrics) 'catalog_backend_reads_total'
    Start-Sleep -Milliseconds 600
    [void](Invoke-Client @('list'))
    $ttlExpired = Get-Metric (Get-Metrics) 'catalog_backend_reads_total'
    Assert-That ($ttlWarm -eq 1 -and $ttlExpired -eq 2) "TTL cache reads after expiry were $ttlWarm then $ttlExpired; expected 1 then 2"
    Add-Proof 'TTL expiry' 'warm TTL cache; wait 600ms for configured 400ms TTL; list again' "gateway backend reads advanced from $ttlWarm to $ttlExpired"

    Stop-CatalogProcess $script:gateway
    Stop-CatalogProcess $script:database
    $script:database = Start-Database '500ms'
    Wait-DatabaseTcp $dbPort
    $script:gateway = Start-Gateway 'ttl' '1s'
    [void](Wait-Gateway $adminPort)
    $raceRevision = [uint64]2
    $startLines = @(Get-Content $script:database.Stdout -ErrorAction SilentlyContinue).Count
    $racingList = Start-BackgroundList 'ttl-stale-fill-request'
    Wait-SnapshotCaptured $script:database $raceRevision $startLines
    $ttlRaceWrite = Update-Catalog 'P24 TTL fresh catalog' $raceRevision
    Assert-That ($ttlRaceWrite.revision -eq 3) 'TTL race update did not commit revision 3'
    Assert-That ($racingList.WaitForExit(5000)) 'TTL stale-fill request did not finish within five seconds'
    $oldResponse = Get-Content -Raw (Join-Path $evidenceDir 'ttl-stale-fill-request.stdout.log') | ConvertFrom-Json
    $staleCache = (Invoke-Client @('list') | ConvertFrom-Json)
    Assert-That ($oldResponse.products[0].name -eq 'P24 updated catalog' -and $staleCache.products[0].name -eq 'P24 updated catalog') 'TTL baseline did not reproduce an old in-flight fill after the update'
    Start-Sleep -Milliseconds 1100
    $ttlFresh = (Invoke-Client @('list') | ConvertFrom-Json)
    Assert-That ($ttlFresh.products[0].name -eq 'P24 TTL fresh catalog') 'TTL cache failed to refresh after expiry'
    Add-Proof 'TTL stale-fill reproduction' 'hold captured backend snapshot; update through loopback admin; read cache before/after TTL expiry' "same-process cache served revision-2 data after revision-3 write, then refreshed to $($ttlFresh.products[0].name) after expiry"

    Restart-Gateway 'lease' '1s'
    $startLines = @(Get-Content $script:database.Stdout -ErrorAction SilentlyContinue).Count
    $leaseRequest = Start-BackgroundList 'lease-fenced-fill-request'
    Wait-SnapshotCaptured $script:database 3 $startLines
    $leaseWrite = Update-Catalog 'P24 lease fresh catalog' 3
    Assert-That ($leaseWrite.revision -eq 4) 'lease race update did not commit revision 4'
    Assert-That ($leaseRequest.WaitForExit(5000)) 'fenced lease request did not finish within five seconds'
    $leaseResponse = Get-Content -Raw (Join-Path $evidenceDir 'lease-fenced-fill-request.stdout.log') | ConvertFrom-Json
    $leaseMetrics = Get-Metrics
    Assert-That ($leaseResponse.products[0].name -eq 'P24 lease fresh catalog') 'lease caller received a stale snapshot after write invalidation'
    Assert-That ((Get-Metric $leaseMetrics 'catalog_fenced_fills_total') -eq 1) 'lease mode did not count exactly one fenced stale fill'
    Assert-That ((Get-Metric $leaseMetrics 'catalog_backend_reads_total') -eq 2) 'lease mode did not refetch once after fencing the old fill'
    Add-Proof 'lease fence during stale fill' 'start delayed cold read; wait for captured revision 3; replace revision 3; read result' "returned revision-4 product; fenced fills=1; backend read calls=2"

    Restart-Gateway 'lease' '2s'
    $leaseStatsBefore = (Invoke-Client @('stats') | ConvertFrom-Json)
    $leaseBench = Invoke-Client @('bench', '40', '40') | ConvertFrom-Json
    $leaseMetrics = Get-Metrics
    $leaseStatsAfter = (Invoke-Client @('stats') | ConvertFrom-Json)
    $leaseBackendDelta = $leaseStatsAfter.backendReads - $leaseStatsBefore.backendReads
    Assert-That ($leaseBench.completed -eq 40 -and $leaseBench.failed -eq 0) 'lease cold-miss load did not complete all 40 requests'
    Assert-That ($leaseBackendDelta -eq 1) "lease cold-miss requests caused $leaseBackendDelta physical backend reads, expected one"
    Assert-That ((Get-Metric $leaseMetrics 'catalog_lease_waiters_total') -gt 0) 'lease cold-miss load did not record waiters'
    Add-Proof 'lease cold-miss herd coalescing' 'catalogctl bench 40 40 against a 500ms captured-snapshot response delay' "offered=40 completed=$($leaseBench.completed) failed=$($leaseBench.failed); actual DB reads=$leaseBackendDelta; waiters=$(Get-Metric $leaseMetrics 'catalog_lease_waiters_total')"

    Restart-Gateway 'ttl' '2s'
    $ttlStatsBefore = (Invoke-Client @('stats') | ConvertFrom-Json)
    $ttlBench = Invoke-Client @('bench', '20', '20') | ConvertFrom-Json
    $ttlStatsAfter = (Invoke-Client @('stats') | ConvertFrom-Json)
    $ttlBackendDelta = $ttlStatsAfter.backendReads - $ttlStatsBefore.backendReads
    Assert-That ($ttlBench.completed -eq 20 -and $ttlBench.failed -eq 0) 'TTL cold-miss load did not complete all 20 requests'
    Assert-That ($ttlBackendDelta -gt 1) "TTL baseline cold misses caused only $ttlBackendDelta backend reads; expected an observable herd"
    Add-Proof 'TTL cold-miss herd baseline' 'catalogctl bench 20 20 against the same 500ms response delay' "offered=20 completed=$($ttlBench.completed) failed=$($ttlBench.failed); actual DB reads=$ttlBackendDelta"

    Restart-Gateway 'lease' '2s'
    [void](Invoke-Client @('list'))
    $beforeCacheDeath = Get-Metric (Get-Metrics) 'catalog_backend_reads_total'
    Restart-Gateway 'lease' '2s'
    [void](Invoke-Client @('list'))
    $afterCacheDeath = Get-Metric (Get-Metrics) 'catalog_backend_reads_total'
    Assert-That ($beforeCacheDeath -eq 1 -and $afterCacheDeath -eq 1) 'new gateway process did not rebuild an empty in-memory cache from the backend'
    Add-Proof 'cache-process death and rebuild' 'warm lease gateway; stop its process; start a fresh lease gateway; ListProducts' "each process made one backend read to build its own cache"

    Stop-CatalogProcess $script:gateway
    Stop-CatalogProcess $script:database
    $script:database = Start-Database '0s'
    [void](Wait-Database $dbPort)
    $runOrder = @('ttl', 'lease', 'none', 'lease', 'ttl', 'none')
    foreach ($mode in $runOrder) {
        $script:gateway = Start-Gateway $mode '5s'
        [void](Wait-Gateway $adminPort)
        [void](Invoke-Client @('list'))
        $measurement = Invoke-Client @('bench', '100', '16') | ConvertFrom-Json
        Assert-That ($measurement.completed -eq 100 -and $measurement.failed -eq 0) "$mode warmed run did not complete 100 requests"
        $benchmarks.Add([ordered]@{ mode = $mode; warm = $true; injected_delay = '0s'; result = $measurement })
        Stop-CatalogProcess $script:gateway
    }
    Add-Proof 'warmed interleaved latency measurements' 'run catalogctl bench 100 16 in ttl,lease,none,lease,ttl,none order after one warm request each' 'each run records offered/completed/failed and actual p50/p95/p99 gRPC request latency; no SLO threshold is asserted'

    $result = 'passed'
} catch {
    $failure = $_.Exception.Message
    throw
} finally {
    $cleanupFailures = [System.Collections.Generic.List[string]]::new()
    foreach ($entry in $started) {
        try { Stop-CatalogProcess $entry }
        catch { $cleanupFailures.Add("stopping $($entry.Name): $($_.Exception.Message)") }
    }
    try {
        $resolvedWork = [System.IO.Path]::GetFullPath($workDir)
        $directorySeparator = [System.IO.Path]::DirectorySeparatorChar
        $resolvedTemp = [System.IO.Path]::GetFullPath($tempRoot).TrimEnd('\', '/') + $directorySeparator
        if ($resolvedWork.StartsWith($resolvedTemp, [System.StringComparison]::OrdinalIgnoreCase) -and (Test-Path $resolvedWork)) {
            Remove-Item -LiteralPath $resolvedWork -Recurse -Force -ErrorAction Stop
        } elseif (Test-Path $resolvedWork) {
            $cleanupFailures.Add("refused cleanup outside the temporary directory: $resolvedWork")
        }
    } catch {
        $cleanupFailures.Add("removing owned temporary work directory: $($_.Exception.Message)")
    }
    if ($cleanupFailures.Count -gt 0) {
        $result = 'failed'
        $cleanupMessage = $cleanupFailures -join '; '
        $failure = if ($failure) { "$failure; cleanup: $cleanupMessage" } else { "cleanup: $cleanupMessage" }
    }
    New-Item -ItemType Directory -Force -Path $evidenceDir | Out-Null
    $logPaths = @(Get-ChildItem -LiteralPath $evidenceDir -File | Select-Object -ExpandProperty Name)
    $survivesCleanup = (Test-Path $evidenceDir) -and ($logPaths.Count -gt 0)
    if ($env:GITHUB_OUTPUT) {
        try { "evidence_path=reports/evidence/$stamp" | Add-Content -LiteralPath $env:GITHUB_OUTPUT -Encoding utf8 -ErrorAction Stop }
        catch {
            $result = 'failed'
            $failure = if ($failure) { "$failure; writing GitHub evidence output: $($_.Exception.Message)" } else { "writing GitHub evidence output: $($_.Exception.Message)" }
        }
    }
    $summary = [ordered]@{
        result = $result
        failure = $failure
        started_utc = $stamp
        completed_utc = [DateTime]::UtcNow.ToString('o')
        upstream_commit = (& git -C $projectRoot rev-parse HEAD).Trim()
        go_version = (& $go version)
        cache_modes = @('none', 'ttl', 'lease')
        database_response_delay_for_controlled_races = '500ms, after snapshot capture'
        measurements = $benchmarks
        proofs = $proofs
        surviving_evidence_files = $logPaths
        evidence_survived_cleanup = $survivesCleanup
    }
    $summary | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath (Join-Path $evidenceDir 'evidence.json') -Encoding utf8NoBOM
}

Write-Output "P24 catalog verification $result. Evidence: $evidenceDir"
if ($result -ne 'passed') { exit 1 }
