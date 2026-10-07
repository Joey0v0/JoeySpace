param()

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$runDir = Join-Path $env:TEMP ('codex-stage7-http-' + [guid]::NewGuid().ToString('N'))
$container = 'codex-stage7-http-' + [guid]::NewGuid().ToString('N')
$mysqlPass = [guid]::NewGuid().ToString('N')
$jwtSecret = [guid]::NewGuid().ToString('N')
$imProcess = $null
$apiProcess = $null

function New-FreePort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    return $port
}

function Test-CommandResult([string]$step) {
    if ($LASTEXITCODE -ne 0) { throw "$step failed (exit $LASTEXITCODE)" }
}

function New-Jwt([string]$secret) {
    $now = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
    $header = '{"alg":"HS256","typ":"JWT"}'
    $payload = @{ user_id = 42; iss = 'go-im'; iat = $now; exp = $now + 3600 } | ConvertTo-Json -Compress
    $base64Url = {
        param([byte[]]$bytes)
        [Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
    }
    $unsigned = (& $base64Url ([Text.Encoding]::UTF8.GetBytes($header))) + '.' + (& $base64Url ([Text.Encoding]::UTF8.GetBytes($payload)))
    $hmac = [Security.Cryptography.HMACSHA256]::new([Text.Encoding]::UTF8.GetBytes($secret))
    try { $signature = & $base64Url ($hmac.ComputeHash([Text.Encoding]::UTF8.GetBytes($unsigned))) }
    finally { $hmac.Dispose() }
    return "$unsigned.$signature"
}

function Wait-LocalPort([int]$port, [string]$step) {
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        $client = [Net.Sockets.TcpClient]::new()
        try {
            $client.Connect('127.0.0.1', $port)
            return
        } catch { Start-Sleep -Milliseconds 500 }
        finally { $client.Dispose() }
    }
    throw "$step did not listen on port $port"
}

try {
    New-Item -ItemType Directory -Path $runDir | Out-Null
    $imPort = New-FreePort
    $apiPort = New-FreePort
    while ($apiPort -eq $imPort) { $apiPort = New-FreePort }
    $imConfig = Join-Path $runDir 'im.yaml'
    $apiConfig = Join-Path $runDir 'api.yaml'
    $utf8 = [Text.UTF8Encoding]::new($false)
    [IO.File]::WriteAllText($imConfig, (Get-Content -Raw (Join-Path $root 'rpc/im/etc/im.yaml')).Replace('127.0.0.1:9002', "127.0.0.1:$imPort"), $utf8)
    [IO.File]::WriteAllText($apiConfig, (Get-Content -Raw (Join-Path $root 'api/etc/api.yaml')).Replace('Port: 8082', "Port: $apiPort").Replace('127.0.0.1:9002', "127.0.0.1:$imPort"), $utf8)
    $imExe = Join-Path $runDir 'im.exe'
    $apiExe = Join-Path $runDir 'api.exe'
    Push-Location $root
    try {
        & go build -o $imExe ./rpc/im
        Test-CommandResult 'build IM'
        & go build -o $apiExe ./api
        Test-CommandResult 'build Gateway'
    } finally { Pop-Location }

    $initSql = (Resolve-Path (Join-Path $root 'deploy/mysql/init.sql')).Path
    $containerId = & docker run -d --rm --name $container -e "MYSQL_ROOT_PASSWORD=$mysqlPass" -v "${initSql}:/docker-entrypoint-initdb.d/init.sql:ro" -p '127.0.0.1::3306' mysql:8.0
    Test-CommandResult 'start isolated MySQL'
    $mysqlPort = [int]((& docker port $container '3306/tcp') -split ':')[-1]
    Test-CommandResult 'inspect isolated MySQL port'
    $ready = $false
    for ($attempt = 0; $attempt -lt 90; $attempt++) {
        $ErrorActionPreference = 'Continue'
        try {
            $null = & docker exec -e "MYSQL_PWD=$mysqlPass" $container mysql -h 127.0.0.1 -uroot -N -e 'SELECT COUNT(*) FROM go_im.messages' 2>$null
            $mysqlExit = $LASTEXITCODE
        } finally { $ErrorActionPreference = 'Stop' }
        if ($mysqlExit -eq 0) { $ready = $true; break }
        Start-Sleep -Seconds 1
    }
    if (-not $ready) { throw 'isolated MySQL did not finish initialization' }
    $seed = "INSERT INTO go_im.messages (id,msg_id,from_id,to_id,chat_type,content_type,content) VALUES (9007199254740997,'http-test-1',43,42,1,1,'one'),(9007199254740998,'http-test-2',42,43,1,1,'two'),(9007199254740999,'http-test-3',43,42,1,1,'three'),(9007199254741000,'http-test-4',44,42,1,1,'other peer');"
    $null = & docker exec -e "MYSQL_PWD=$mysqlPass" $container mysql -h 127.0.0.1 -uroot -e $seed
    Test-CommandResult 'seed isolated messages'

    $env:IM_MYSQL_DSN = "root:${mysqlPass}@tcp(127.0.0.1:$mysqlPort)/go_im?charset=utf8mb4&parseTime=True&loc=Local"
    $env:IM_JWT_SECRET = $jwtSecret
    $imProcess = Start-Process -FilePath $imExe -ArgumentList @('-f', $imConfig) -WorkingDirectory $root -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runDir 'im.out') -RedirectStandardError (Join-Path $runDir 'im.err')
    Remove-Item Env:IM_MYSQL_DSN, Env:IM_JWT_SECRET
    Wait-LocalPort $imPort 'IM RPC'
    $apiProcess = Start-Process -FilePath $apiExe -ArgumentList @('-f', $apiConfig) -WorkingDirectory $root -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $runDir 'api.out') -RedirectStandardError (Join-Path $runDir 'api.err')
    Wait-LocalPort $apiPort 'Gateway HTTP'

    $token = New-Jwt $jwtSecret
    $headers = @{ Authorization = "Bearer $token" }
    $base = "http://127.0.0.1:$apiPort/api/v1/direct/43"
    $page = Invoke-RestMethod -Uri "$base/messages?limit=2" -Headers $headers
    if ($page.data.messages.Count -ne 2 -or $page.data.messages[0].id -ne '9007199254740999' -or $page.data.next_before_message_id -ne '9007199254740998') { throw 'history first page mismatch' }
    $older = Invoke-RestMethod -Uri "$base/messages?before_message_id=9007199254740998&limit=2" -Headers $headers
    if ($older.data.messages.Count -ne 1 -or $older.data.messages[0].id -ne '9007199254740997') { throw 'history older page mismatch' }
    $unread = Invoke-RestMethod -Uri "$base/unread" -Headers $headers
    if ($unread.data.unread_count -ne '2') { throw 'initial unread mismatch' }
    $body = '{"message_ids":["9007199254740997"]}'
    $marked = Invoke-RestMethod -Method Post -Uri "$base/read" -Headers $headers -ContentType 'application/json' -Body $body
    if ($marked.data.unread_count -ne '1') { throw 'mark read mismatch' }
    $replayed = Invoke-RestMethod -Method Post -Uri "$base/read" -Headers $headers -ContentType 'application/json' -Body $body
    if ($replayed.data.unread_count -ne '1') { throw 'replay mismatch' }
    $badBody = '{"message_ids":["9007199254740999","9007199254741000"]}'
    try {
        $null = Invoke-RestMethod -Method Post -Uri "$base/read" -Headers $headers -ContentType 'application/json' -Body $badBody
        throw 'wrong-scope batch unexpectedly succeeded'
    } catch {
        if ($_.Exception.Response.StatusCode.value__ -ne 404) { throw }
    }
    $after = Invoke-RestMethod -Uri "$base/unread" -Headers $headers
    if ($after.data.unread_count -ne '1') { throw 'wrong-scope batch changed unread count' }
    Write-Output 'PASS: Gateway HTTP -> IM gRPC -> isolated MySQL: paging, unread, explicit read, replay, wrong-scope rollback.'
} catch {
    foreach ($name in @('im.err', 'api.err', 'im.out', 'api.out')) {
        $path = Join-Path $runDir $name
        if (Test-Path -LiteralPath $path) {
            $lines = (Get-Content -LiteralPath $path -Tail 12) -join "`n"
            if ($lines) {
                Write-Output ("${name}: " + $lines.Replace($mysqlPass, '[redacted]').Replace($jwtSecret, '[redacted]'))
            }
        }
    }
    throw
} finally {
    Remove-Item Env:IM_MYSQL_DSN, Env:IM_JWT_SECRET -ErrorAction SilentlyContinue
    if ($apiProcess) { Stop-Process -Id $apiProcess.Id -Force -ErrorAction SilentlyContinue }
    if ($imProcess) { Stop-Process -Id $imProcess.Id -Force -ErrorAction SilentlyContinue }
    if ($containerId) {
        $ErrorActionPreference = 'Continue'
        try { $null = & docker rm -f $container 2>$null }
        finally { $ErrorActionPreference = 'Stop' }
    }
    if (Test-Path -LiteralPath $runDir) {
        $resolved = (Resolve-Path -LiteralPath $runDir).Path
        $tempRoot = (Resolve-Path -LiteralPath $env:TEMP).Path.TrimEnd('\') + '\'
        if (-not $resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or -not ((Split-Path -Leaf $resolved) -like 'codex-stage7-http-*')) { throw 'refusing to remove unexpected test directory' }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
