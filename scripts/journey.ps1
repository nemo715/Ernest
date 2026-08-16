# journey.ps1 — the complete ernest user journey (plan Task 6)
#
#   Step 0  preflight: binary + UI build + toolchain probes
#   Step 1  fresh temp dir -> `ernest new agent` -> `ernest run` (mock, no
#           keys) -> session file + output verified
#   Step 1b config-driven teams + workflows: `ernest run --team editorial`
#           and `ernest run --workflow pipeline` (no Go code)
#   Step 1c Python authoring: `python -m ernest run crew.py` (DSL -> Go engine)
#   Step 1d tool packs: scripted mock loop runs file_read + web_search inside
#           a sandbox (guardrails: sandbox only, shell off by default)
#   Step 2  `ernest eval` with mock scenarios -> pass/fail report shown
#   Step 3  `ernest new knowledge` -> ingest docs -> chat with real provider
#           -> run trace context shows retrieved chunks
#   Step 4  `ernest new quantum` -> playground on :9090 ->
#           `python orchestrate.py` -> executed notebook + fidelity numbers
#   Step 5  `ernest playground --static web/out` -> /healthz, /api/agents,
#           /api/runs, feedback roundtrip; UI pages render
#   Step 5b server orchestration roundtrip: GET /api/teams + /api/workflows,
#           POST /api/teams/{name}/run + /api/workflows/{name}/run over SSE
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

# Locate a Go toolchain: PATH first, then common install roots (the Go
# installer does not always add itself to PATH).
function Find-Go {
    $g = (Get-Command go -ErrorAction SilentlyContinue).Source
    if ($g) { return $g }
    foreach ($cand in @("$env:LOCALAPPDATA\Programs\Go\bin\go.exe", "C:\Go\bin\go.exe", "$env:ProgramFiles\Go\bin\go.exe")) {
        if (Test-Path $cand) { return $cand }
    }
    return ""
}

# ---------------------------------------------------------------------------
# Step 0 — preflight
# ---------------------------------------------------------------------------
Step "Step 0: preflight (binary, UI build, python/qiskit, API key)"

if (-not (Test-Path $ErnestExe)) {
    $goBin = Find-Go
    if (-not $goBin) { Log "FAIL: no go toolchain found and no $ErnestExe"; exit 99 }
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
# Step 1b — config-driven teams + workflows (no Go code)
# ---------------------------------------------------------------------------
Step "Step 1b: ernest run --team / --workflow from ernest.json"

$dOrch = Join-Path $Scratch "orch"
New-Item -ItemType Directory -Force -Path $dOrch | Out-Null
@'
{
  "agents": [
    { "name": "lead", "provider": "mock", "model": "mock-1", "instructions": "You coordinate the team." },
    { "name": "researcher", "provider": "mock", "model": "mock-1", "instructions": "You research topics." },
    { "name": "writer", "provider": "mock", "model": "mock-1", "instructions": "You write clearly." }
  ],
  "teams": [
    { "name": "editorial", "description": "Sequential research team", "leader": "lead", "members": ["researcher", "writer"], "process": "sequential" }
  ],
  "workflows": [
    {
      "name": "pipeline",
      "description": "Research then write",
      "steps": [
        { "name": "research", "agent": "researcher", "prompt": "Research {{input}}" },
        { "name": "write", "agent": "writer", "prompt": "Write from {{research}}", "dependsOn": ["research"] }
      ]
    }
  ]
}
'@ | Out-File -FilePath (Join-Path $dOrch "ernest.json") -Encoding ascii

$teamRun = Invoke-Native $dOrch "ernest run --team editorial --input `"plan the release`" --json" {
    & $ErnestExe run --config ernest.json --team editorial --input "plan the release" --json
}
$teamText = $teamRun -join "`n"
Check ($teamText -match '"status":\s*"completed"') "team run completed"
Check ($teamText -match '"team":\s*"editorial"') "team run carries team metadata"
Check ($teamText -match '"process":\s*"sequential"') "team run used the sequential process"
Check ($teamText -match '"members":\s*\[\s*"researcher"') "team run metadata lists members"
Check ($teamText -match "Hello from the mock provider") "team output reached the user"

$wfRun = Invoke-Native $dOrch "ernest run --workflow pipeline --input `"Go concurrency`" --json" {
    & $ErnestExe run --config ernest.json --workflow pipeline --input "Go concurrency" --json
}
$wfText = $wfRun -join "`n"
Check ($wfText -match '"status":\s*"completed"') "workflow run completed"
Check ($wfText -match 'research') "workflow state carries the research step output"
Check ($wfText -match 'write') "workflow state carries the write step output"
Check ($wfText -match 'Go concurrency') "workflow state carries the input"

# ---------------------------------------------------------------------------
# Step 1c — Python authoring (python -m ernest run crew.py)
# ---------------------------------------------------------------------------
Step "Step 1c: python -m ernest run crew.py (Python authoring)"

if ($py) {
    $dPy = Join-Path $Scratch "pycrew"
    New-Item -ItemType Directory -Force -Path $dPy | Out-Null
    @'
from ernest import Agent, Task, Crew

researcher = Agent("researcher", provider="mock", instructions="You research topics.")
writer = Agent("writer", provider="mock", instructions="You write clearly.")
research = Task(researcher, "Research {{input}}", name="research")
write = Task(writer, "Write from {{research}}", name="write", depends_on=["research"])
crew = Crew(name="py-crew", tasks=[research, write])
'@ | Out-File -FilePath (Join-Path $dPy "crew.py") -Encoding ascii

    $pyRun = Invoke-Native $dPy "python -m ernest run crew.py --input `"quantum chips`" --json (ERNEST_BIN + PYTHONPATH)" {
        $env:ERNEST_BIN = $ErnestExe
        $env:PYTHONPATH = (Join-Path $repo "python") + [IO.Path]::PathSeparator + $env:PYTHONPATH
        & python -m ernest run crew.py --input "quantum chips" --json
    }
    $pyText = $pyRun -join "`n"
    Check ($pyText -match '"status":\s*"completed"') "python -m ernest run crew.py completed"
    Check ($pyText -match 'research') "python crew ran the research step"
    Check ($pyText -match 'quantum chips') "python crew input reached the workflow"

    $pyDoctor = Invoke-Native $dPy "python -m ernest doctor crew.py --json" {
        $env:ERNEST_BIN = $ErnestExe
        $env:PYTHONPATH = (Join-Path $repo "python") + [IO.Path]::PathSeparator + $env:PYTHONPATH
        & python -m ernest doctor crew.py --json
    }
    $pyDocText = $pyDoctor -join "`n"
    Check (($pyDocText -match '"workflows"') -and ($pyDocText -match '"py-crew"')) "python -m ernest doctor validates the compiled crew config"
} else {
    Log "SKIP: no python on PATH — python -m ernest not attempted"
}

# ---------------------------------------------------------------------------
# Step 1d — tool packs: scripted mock loop with file_read + web_search
# ---------------------------------------------------------------------------
Step "Step 1d: tool packs (file_read + web_search, sandboxed, scripted mock)"

$goBin2 = Find-Go
Log "go toolchain: $goBin2"
if ($goBin2) {
    $dTools = Join-Path $Scratch "toolpack"
    New-Item -ItemType Directory -Force -Path $dTools | Out-Null
    @'
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/nemo715/Ernest/agent"
	"github.com/nemo715/Ernest/core"
	"github.com/nemo715/Ernest/llm"
)

func main() {
	// Deterministic search endpoint (no network, no keys).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<div class="result"><a class="result__a" href="https://example.com/x">Result X</a><a class="result__snippet" href="https://example.com/x">snippet x</a></div>`)
	}))
	defer srv.Close()
	os.Setenv("ERNEST_WEB_SEARCH_URL", srv.URL)

	dir, err := os.MkdirTemp("", "ernest-journey-sandbox-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("sandbox note"), 0o644); err != nil {
		panic(err)
	}

	// Scripted mock provider: one turn of tool calls, then a final answer.
	p := llm.NewMock(llm.MockConfig{Script: []llm.MockTurn{
		{ToolCalls: []core.ToolCall{
			{ID: "r1", Name: "file_read", Arguments: []byte(`{"path":"note.txt"}`)},
			{ID: "s1", Name: "web_search", Arguments: []byte(`{"query":"ernest"}`)},
		}, FinishReason: "tool_calls"},
		{Content: "tool pack verified", FinishReason: "stop"},
	}})
	a := agent.New("worker", p)
	byName := core.ToolsByName(core.BuiltinTools)
	a.Tools = []*core.Tool{byName["file_read"], byName["file_list"], byName["web_search"]}
	a.ToolSandbox = dir

	res, err := a.Chat(context.Background(), "check the sandbox", agent.RunOptions{SessionID: "journey-toolpack"})
	if err != nil {
		fmt.Println("FATAL:", err)
		os.Exit(1)
	}
	if res.Status != core.RunStatusCompleted {
		fmt.Println("FATAL: status", res.Status)
		os.Exit(1)
	}
	joined := ""
	for _, m := range res.Messages {
		joined += m.Content
	}
	for _, want := range []string{"sandbox note", "Result X", "snippet x"} {
		if !strings.Contains(joined, want) {
			fmt.Println("FATAL: tool result missing:", want, joined)
			os.Exit(1)
		}
	}
	fmt.Println("tool pack verified: file_read returned the sandbox note; web_search returned Result X")
}
'@ | Out-File -FilePath (Join-Path $dTools "main.go") -Encoding ascii

    $mod = "module journeytoolpack`r`ngo 1.26`r`n`r`nrequire github.com/nemo715/Ernest v0.0.0`r`n`r`nreplace github.com/nemo715/Ernest => " + ($repo -replace '\\', '/')
    [System.IO.File]::WriteAllText((Join-Path $dTools "go.mod"), $mod, (New-Object System.Text.UTF8Encoding($false)))

    $tidy = Invoke-Native $dTools "go mod tidy (offline, replaced with the local repo)" { & $goBin2 mod tidy }
    Check ($LASTEXITCODE -eq 0) "tool pack module resolves against the local repo"
    $toolRun = Invoke-Native $dTools "go run . (scripted mock calls file_read + web_search)" { & $goBin2 run . }
    $toolText = $toolRun -join "`n"
    Check ($toolText -match "tool pack verified") "tool pack run completed (file_read + web_search through the run loop)"
    Check ($toolText -match "sandbox note") "file_read returned the sandboxed file"
    Check ($toolText -match "Result X") "web_search returned parsed results"
} else {
    Log "SKIP: no go toolchain found — tool pack run not attempted"
}

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
# Step 5b — server orchestration roundtrip (teams + workflows over SSE)
# ---------------------------------------------------------------------------
Step "Step 5b: server /api/teams + /api/workflows SSE roundtrip"

$srv5bOut = Join-Path $Scratch "play-9092.out.log"
$srv5bErr = Join-Path $Scratch "play-9092.err.log"
$p5b = Start-Process -FilePath $ErnestExe -ArgumentList "playground", "--config", "ernest.json", "--port", "9092" `
    -WorkingDirectory $dOrch -RedirectStandardOutput $srv5bOut -RedirectStandardError $srv5bErr -PassThru -WindowStyle Hidden
Log "playground (orchestration) pid $($p5b.Id) on :9092"
Check (Wait-Healthy "http://127.0.0.1:9092/healthz" 90) "playground healthz up"

$teams = Get-HTTP "http://127.0.0.1:9092/api/teams"
Check ($teams -match '"name":\s*"editorial"') "GET /api/teams lists the editorial team"
$wfs = Get-HTTP "http://127.0.0.1:9092/api/workflows"
Check ($wfs -match '"name":\s*"pipeline"') "GET /api/workflows lists the pipeline workflow"

$teamBody = Join-Path $Scratch "team-run-body.json"
[System.IO.File]::WriteAllText($teamBody, '{"input":"plan the server run"}', (New-Object System.Text.UTF8Encoding($false)))
$teamSSE = curl.exe -s -X POST -H "Content-Type: application/json" -d "@$teamBody" "http://127.0.0.1:9092/api/teams/editorial/run"
$teamSSEText = $teamSSE -join "`n"
Log "POST /api/teams/editorial/run -> $($teamSSE.Count) SSE frames"
Check ($teamSSEText -match '"type":\s*"run.start"') "team run SSE starts with run.start"
Check ($teamSSEText -match '"type":\s*"run.complete"') "team run SSE ends with run.complete"
Check ($teamSSEText -match '"editorial"') "team run SSE carries the team name"
Check ($teamSSEText -match "Hello from the mock provider") "team run SSE delivered member output"

$wfBody = Join-Path $Scratch "wf-run-body.json"
[System.IO.File]::WriteAllText($wfBody, '{"input":"server pipelines"}', (New-Object System.Text.UTF8Encoding($false)))
$wfSSE = curl.exe -s -X POST -H "Content-Type: application/json" -d "@$wfBody" "http://127.0.0.1:9092/api/workflows/pipeline/run"
$wfSSEText = $wfSSE -join "`n"
Log "POST /api/workflows/pipeline/run -> $($wfSSE.Count) SSE frames"
Check ($wfSSEText -match '"type":\s*"step.start"') "workflow run SSE streams step.start"
Check ($wfSSEText -match '"research"') "workflow run SSE streams the research step"
Check ($wfSSEText -match '"write"') "workflow run SSE streams the write step"
Check ($wfSSEText -match '"type":\s*"run.complete"') "workflow run SSE ends with run.complete"
Check ($wfSSEText -match 'server pipelines') "workflow run SSE carried the input into the state"

$missTeam = curl.exe -s -m 10 -o NUL -w "%{http_code}" -X POST -H "Content-Type: application/json" -d "@$teamBody" "http://127.0.0.1:9092/api/teams/ghost/run"
Check ($missTeam -eq "404") "unknown team run returns 404"
if ($p5b -and -not $p5b.HasExited) { Stop-Process -Id $p5b.Id -Force }

# ---------------------------------------------------------------------------
# Step 6 — doctor clean at every stage
# ---------------------------------------------------------------------------
Step "Step 6: ernest doctor clean at every stage"

foreach ($d in @($d1, $dOrch, $d3, $d4)) {
    $doc = Invoke-Native $d "ernest doctor (in $(Split-Path -Leaf $d))" { & $ErnestExe doctor }
    Check ($LASTEXITCODE -eq 0) "doctor clean in $(Split-Path -Leaf $d)"
}

# ---------------------------------------------------------------------------
Log "`n=================================================================="
Log "== Journey complete: $script:fails failure(s)"
Log "=================================================================="
Write-Host "`ntranscript: $log"
exit $script:fails
