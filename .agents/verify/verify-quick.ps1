# Quick re-run (ASCII-only, see verify.ps1): vet + service unit tests + race on the touched video/image paths.
$ErrorActionPreference = 'Continue'
$Root = 'C:\Users\ASUS\Desktop\TokenRouter'
$Log = Join-Path $Root '.agents\verify\logs-quick'
New-Item -ItemType Directory -Force -Path $Log | Out-Null
Get-ChildItem -Path $Log -Filter *.log -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
$Summary = Join-Path $Log 'summary.log'

# Run one step, write its output to its own log and record PASS/FAIL in the summary.
function Invoke-Step {
    param([string]$Name, [string]$WorkDir, [string]$Command)
    $line = ">>> $Name $(Get-Date -Format HH:mm:ss)"
    Write-Host $line
    Add-Content -Path $Summary -Value $line
    $out = Join-Path $Log "$Name.log"
    Push-Location $WorkDir
    $global:LASTEXITCODE = 0
    Invoke-Expression "$Command *> '$out'"
    $code = $LASTEXITCODE
    Pop-Location
    if ($code -eq 0) {
        $result = "PASS $Name"
    } else {
        $result = "FAIL $Name (exit $code, see $out)"
    }
    Write-Host $result
    Add-Content -Path $Summary -Value $result
}

$Backend = Join-Path $Root 'backend'
Invoke-Step 'build' $Backend 'go build ./...'
Invoke-Step 'unit_service' $Backend 'go test -tags=unit -p 1 -count=1 -timeout 40m ./internal/service/'
$env:CGO_ENABLED = '1'
Invoke-Step 'race_video' $Backend 'go test -tags=unit -race -p 1 -count=1 -timeout 30m -run Video ./internal/service/'
Remove-Item Env:CGO_ENABLED
$done = "done $(Get-Date -Format HH:mm:ss)"
Write-Host $done
Add-Content -Path $Summary -Value $done
