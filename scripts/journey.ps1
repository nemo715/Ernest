# journey.ps1 — the complete ernest user journey (plan Task 6)
#
#   Step 1  fresh temp dir -> `ernest new agent` -> `ernest run` (mock, no
#           keys) -> session file + output verified
#   Step 2  `ernest eval` with mock scenarios -> pass/fail report shown
#   Step 3  `ernest new knowledge` -> ingest docs -> chat with real provider
#           -> run trace context shows retrieved chunks
#   Step 4  `ernest new quantum` -> playground on :9090 ->
#           `python orchestrate.py` -> executed notebook + fidelity numbers
#   Step 5  `ernest playground --static web/out` -> /healthz, /api/agents,
#           /api/runs, feedback roundtrip; UI pages render
#   Step 6  `ernest doctor` clean at every stage
#
# Every step prints the real command and its real output. The exit code is
# the number of failed checks (0 = all green). A full transcript is written
# to $Scratch\journey.log.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts/journey.ps1
#
# Optional parameters:
#   -ErnestExe  path to the ernest binary (default: <repo>\ernest.exe, built
#               fresh if missing)
#   -StaticDir  built UI directory (default: <repo>\web\out)
#   -Scratch    working directory for the journey (default: fresh %TEMP% dir)
param(
    [string]$ErnestExe = "",
    [string]$StaticDir = "",
    [string]$Scratch = ""
)
$ErrorActionPreference = "Continue"

$repo = Split-Path -Parent $PSScriptRoot
if (-not $ErnestExe) { $ErnestExe = Join-Path $repo "ernest.exe" }
if (-not $StaticDir) { $StaticDir = Join-Path $repo "web\out" }
if (-not $Scratch)   { $Scratch = Join-Path $env:TEMP ("ernest-journey-" + [guid]::NewGuid().ToString("N")) }
New-Item -ItemType Directory -Force -Path $Scratch | Out-Null
$log = Join-Path $Scratch "journey.log"
"journey transcript: $log"
"scratch dir:        $Scratch"

$script:fails = 0
function Log([string]$s) { Write-Host $s; $s | Out-File -FilePath $log -Append -Encoding utf8 }

function Step([string]$title) { Log "`n=================================================================="; Log "== $title"; Log "==================================================================" }

# Run a native command in a working directory, logging the real output.
# Native failures are NOT fatal: they are recorded (stderr + exit code) and
# the caller decides via Check(). With $ErrorActionPreference=Continue,
# PowerShell 5.1 surfaces stderr as error records inside $out, so we also
# catch the NativeCommandException variant and turn it into an exit code.
function Invoke-Native([string]$dir, [string]$what, [scriptblock]$sb) {
    Log "`n> $what"
    Push-Location $dir
    try {
        $out = & $sb 2>&1
        $code = $LASTEXITCODE
    } catch [System.Management.Automation.NativeCommandException] {
        $out = $_.Exception.Message
        $code = $LASTEXITCODE
        if ($code -eq 0) { $code = 1 }
    } finally {
        Pop-Location
    }
    foreach ($line in $out) { Log ([string]$line) }
    if ($code -ne 0) { Log "!! command exited $code" }
    return , $out
}

function Check([bool]$cond, [string]$msg) {
    if ($cond) { Log "PASS: $msg" }
    else       { $script:fails++; Log "FAIL: $msg" }
}

# Poll an HTTP endpoint until it answers or we time out.
function Wait-Healthy([string]$url, [int]$timeoutSec = 90) {
    $deadline = (Get-Date).AddSeconds($timeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $r = curl.exe -s -m 3 -o NUL -w "%{http_code}" $url
            if ($r -eq "200") { return $true }
        } catch { }
        Start-Sleep -Seconds 2
    }
    return $false
}

# GET an HTTP resource; returns the body string ("" on failure).
# -L follows the static server's 301 directory redirects (e.g. /runs -> /runs/).
function Get-HTTP([string]$url) {
    try {
        $body = curl.exe -s -L -m 30 $url
        if ($LASTEXITCODE -ne 0) { return "" }
        return ($body -join "`n")
    } catch { return "" }
}

# ---------------------------------------------------------------------------
# Step 0 — preflight
# ---------------------------------------------------------------------------
Step "Step 0: preflight (binary, UI build, python/qiskit, API key)"

if (-not (Test-Path $ErnestExe)) {
    $goBin = (Get-Command go -ErrorAction SilentlyContinue).Source
    if (-not $goBin) { Log "FAIL: no go toolchain on PATH and no $ErnestExe"; exit 99 }
    Invoke-Native $repo "go build -o $ErnestExe ./cmd/ernest" { & $goBin build -o $ErnestExe ./cmd/ernest }
    Check ((Test-Path $ErnestExe) -and $LASTEXITCODE -eq 0) "ernest binary built at $ErnestExe"
} else {
    Log "using existing binary $ErnestExe"
}
Check (Test-Path (Join-Path $StaticDir "index.html")) "static UI present at $StaticDir"

$py = (Get-Command python -ErrorAction SilentlyContinue).Source
$hasKey = [bool]$env:OPENROUTER_API_KEY
if ($py) {
    $qiskit = python -c "import qiskit; print(qiskit.__version__)" 2>$null
    Log "python: $py | qiskit: $qiskit"
} else {
    Log "WARN: no python on PATH (steps 3-4 fall back to API-only checks)"
}
Log "OPENROUTER_API_KEY set: $hasKey"

# ---------------------------------------------------------------------------
# Step 1 — new agent + mock run
# ---------------------------------------------------------------------------
Step "Step 1: ernest new agent -> ernest run (mock, no keys)"

$d1 = Join-Path $Scratch "agent"
Invoke-Native $repo "ernest new agent $d1" { & $ErnestExe new agent $d1 }
Check ((Test-Path (Join-Path $d1 "ernest.json"))) "ernest.json scaffolded"

$runJson = Invoke-Native $d1 "ernest run --config ernest.json --input `"hello`" --json" {
    & $ErnestExe run --config ernest.json --input "hello" --json
}
$runText = $runJson -join "`n"
Check ($runText -match "Hello from the mock provider") "mock output reached the user"
Check ($runText -match '"runId"') "run id present in JSON result"
Check ((Test-Path (Join-Path $d1 "ernest.db"))) "session store (ernest.db) created"

$runId = ""
if ($runText -match '"runId":\s*"([^"]+)"') { $runId = $Matches[1] }
Log "run id: $runId"

# ---------------------------------------------------------------------------
# Step 2 — eval with mock scenarios
# ---------------------------------------------------------------------------
Step "Step 2: ernest eval with mock scenarios (pass/fail report)"

$scen = Join-Path $d1 "scenarios-mock.json"
@'
{
  "scenarios": [
    { "name": "mock-greets", "input": "hello", "expect": { "status": "completed", "outputContains": ["Hello from the mock provider"] } },
    { "name": "mock-advises", "input": "6*7?", "expect": { "status": "completed", "outputContains": ["mock provider"] } }
  ]
}
'@ | Out-File -FilePath $scen -Encoding ascii

$evalOut = Invoke-Native $d1 "ernest eval --config ernest.json --scenarios scenarios-mock.json --json" {
    & $ErnestExe eval --config ernest.json --scenarios scenarios-mock.json --json
}
$evalText = $evalOut -join "`n"
Check ($evalText -match '"failed":\s*0') "eval: 0 failed scenarios"
Check ($evalText -match '"passed":\s*2') "eval: 2 passed scenarios"

# ---------------------------------------------------------------------------
# Step 3 — knowledge template + real-provider RAG
# ---------------------------------------------------------------------------
Step "Step 3: ernest new knowledge -> ingest -> real-provider chat with retrieval"

$d3 = Join-Path $Scratch "knowledge"
Invoke-Native $repo "ernest new knowledge $d3" { & $ErnestExe new knowledge $d3 }
Check ((Test-Path (Join-Path $d3 "docs\guide.md"))) "knowledge docs/ scaffolded"
Check ((Test-Path (Join-Path $d3 ".env.example"))) ".env.example scaffolded"

if ($hasKey) {
    $ragJson = Invoke-Native $d3 "ernest run --config ernest.json --input `"Can I get a refund on a `$600 order?`" --json" {
        & $ErnestExe run --config ernest.json --input "Can I get a refund on a `$600 order?" --json
    }
    $ragText = $ragJson -join "`n"
    Check ($ragText -match 'Refunds over \$500 require manager approval') "retrieved chunk present in run trace context (context.knowledge)"
    Check ($ragText -match '"status":\s*"completed"') "run completed against the real provider"
} else {
    Log "SKIP: no OPENROUTER_API_KEY — real-provider chat not attempted"
}

# ---------------------------------------------------------------------------
# Step 4 — quantum template + orchestrate.py
# ---------------------------------------------------------------------------
Step "Step 4: ernest new quantum -> playground :9090 -> python orchestrate.py"

$d4 = Join-Path $Scratch "quantum"
Invoke-Native $repo "ernest new quantum $d4" { & $ErnestExe new quantum $d4 }
Check ((Test-Path (Join-Path $d4 "orchestrate.py"))) "orchestrate.py scaffolded"
Check ((Test-Path (Join-Path $d4 "scenarios-quantum.json"))) "scenarios-quantum.json scaffolded"

$srv4Out = Join-Path $Scratch "play-9090.out.log"
$srv4Err = Join-Path $Scratch "play-9090.err.log"
$p4 = Start-Process -FilePath $ErnestExe -ArgumentList "playground", "--config", "ernest.json", "--port", "9090" `
    -WorkingDirectory $d4 -RedirectStandardOutput $srv4Out -RedirectStandardError $srv4Err -PassThru -WindowStyle Hidden
Log "playground (quantum) pid $($p4.Id) on :9090"
Check (Wait-Healthy "http://127.0.0.1:9090/healthz" 90) "playground healthz up"

if ($py -and $hasKey) {
    $orch = Invoke-Native $d4 "python orchestrate.py --base-url http://127.0.0.1:9090 --out build" {
        & python orchestrate.py --base-url "http://127.0.0.1:9090" --out "build"
    }
    $orchText = $orch -join "`n"
    Check ((Test-Path (Join-Path $d4 "build\quantum-lab.ipynb"))) "executed notebook written to build/quantum-lab.ipynb"
    Check ($orchText -match "fidelity|FIDELITY|verdict|Verdict") "fidelity numbers reported in orchestrator output"
    if (Test-Path (Join-Path $d4 "build\quantum-lab.ipynb")) {
        $nb = Get-Content (Join-Path $d4 "build\quantum-lab.ipynb") -Raw
        Check ($nb -match "execution_count") "notebook contains executed cell outputs"
        Check ($nb -match "fidelity|FIDELITY") "notebook contains fidelity numbers"
    }
} else {
    Log "SKIP: python/qiskit or OPENROUTER_API_KEY missing — orchestrator not run"
}
if ($p4 -and -not $p4.HasExited) { Stop-Process -Id $p4.Id -Force }

# ---------------------------------------------------------------------------
# Step 5 — playground + static UI + feedback
# ---------------------------------------------------------------------------
Step "Step 5: ernest playground --static web/out (API + UI + feedback)"

$srv5Out = Join-Path $Scratch "play-9091.out.log"
$srv5Err = Join-Path $Scratch "play-9091.err.log"
$p5 = Start-Process -FilePath $ErnestExe -ArgumentList "playground", "--config", "ernest.json", "--port", "9091", "--static", $StaticDir `
    -WorkingDirectory $d1 -RedirectStandardOutput $srv5Out -RedirectStandardError $srv5Err -PassThru -WindowStyle Hidden
Log "playground (UI) pid $($p5.Id) on :9091"
Check (Wait-Healthy "http://127.0.0.1:9091/healthz" 90) "playground healthz up"

$h = Get-HTTP "http://127.0.0.1:9091/healthz"
Check ($h -match '"status":\s*"ok"') "GET /healthz -> ok ($h)"

$a = Get-HTTP "http://127.0.0.1:9091/api/agents"
Check ($a -match '"name":\s*"assistant"') "GET /api/agents lists the agent"

# Create a run through the server (POST /api/chat -> SSE), exactly like the
# playground does: the trace is recorded server-side and becomes visible in
# /api/runs. PowerShell 5.1 mangles inline JSON args for native commands, so
# the body is written to a BOM-less file and passed with -d @file.
$chatBody = Join-Path $Scratch "chat-body.json"
[System.IO.File]::WriteAllText($chatBody, '{"agent":"assistant","input":"hello journey"}', (New-Object System.Text.UTF8Encoding($false)))
$sse = curl.exe -s -X POST -H "Content-Type: application/json" -d "@$chatBody" "http://127.0.0.1:9091/api/chat"
$sseText = $sse -join "`n"
Log "POST /api/chat -> $($sse.Count) SSE events"
$runId = ""
if ($sseText -match '"runId":"([^"]+)"') { $runId = $Matches[1] }
Check ($runId -ne "") "POST /api/chat created a run ($runId)"
Check ($sseText -match "Hello from the mock provider") "chat SSE delivered the mock reply"

$r = Get-HTTP "http://127.0.0.1:9091/api/runs"
Check ($r -match "$runId") "GET /api/runs lists the server-created run"

if ($runId) {
    $t = Get-HTTP "http://127.0.0.1:9091/api/runs/$runId/trace"
    Check ($t -match '"context"') "GET /api/runs/$runId/trace includes run context"

    $fbBody = Join-Path $Scratch "feedback-body.json"
    [System.IO.File]::WriteAllText($fbBody, '{"rating":5,"comment":"journey live check"}', (New-Object System.Text.UTF8Encoding($false)))
    $fb = curl.exe -s -m 10 -X POST -H "Content-Type: application/json" -d "@$fbBody" "http://127.0.0.1:9091/api/runs/$runId/feedback"
    Check (($fb -join "") -match 'rating') "POST /api/runs/$runId/feedback accepted"
    $fbr = Get-HTTP "http://127.0.0.1:9091/api/runs/$runId/feedback"
    Check ($fbr -match '"rating":\s*5') "GET /api/runs/$runId/feedback returns the rating"
    Check ($fbr -match "journey live check") "feedback comment persisted"
}

# Static UI pages (SPA + dynamic-route fallback).
foreach ($page in @("/", "/runs", "/agents", "/sessions", "/playground")) {
    $body = Get-HTTP "http://127.0.0.1:9091$page"
    Check ($body -ne "" -and $body -match "<!DOCTYPE html>|<!doctype html>|<html") "UI page $page renders HTML"
}
if ($runId) {
    $det = Get-HTTP "http://127.0.0.1:9091/runs/$runId"
    Check ($det -match "<!DOCTYPE html>|<!doctype html>|<html") "UI run detail /runs/$runId renders (dynamic fallback)"
}
if ($p5 -and -not $p5.HasExited) { Stop-Process -Id $p5.Id -Force }

# ---------------------------------------------------------------------------
# Step 6 — doctor clean at every stage
# ---------------------------------------------------------------------------
Step "Step 6: ernest doctor clean at every stage"

foreach ($d in @($d1, $d3, $d4)) {
    $doc = Invoke-Native $d "ernest doctor (in $(Split-Path -Leaf $d))" { & $ErnestExe doctor }
    Check ($LASTEXITCODE -eq 0) "doctor clean in $(Split-Path -Leaf $d)"
}

# ---------------------------------------------------------------------------
Log "`n=================================================================="
Log "== Journey complete: $script:fails failure(s)"
Log "=================================================================="
Write-Host "`ntranscript: $log"
exit $script:fails
