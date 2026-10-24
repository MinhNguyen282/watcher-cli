# PostToolUse hook: format the edited Go file with gofumpt.
# Falls back to gofmt when gofumpt is not on PATH.

$ErrorActionPreference = 'Stop'

$raw = [Console]::In.ReadToEnd()
if ([string]::IsNullOrWhiteSpace($raw)) { exit 0 }

try { $payload = $raw | ConvertFrom-Json } catch { exit 0 }

$toolInput = $payload.tool_input
if ($null -eq $toolInput) { exit 0 }

$path = $toolInput.file_path
if ([string]::IsNullOrWhiteSpace($path)) { exit 0 }
if ($path -notmatch '\.go$') { exit 0 }

$cwd = $payload.cwd
if ([string]::IsNullOrWhiteSpace($cwd)) { $cwd = (Get-Location).Path }

try {
    if ([System.IO.Path]::IsPathRooted($path)) {
        $full = [System.IO.Path]::GetFullPath($path)
    } else {
        $full = [System.IO.Path]::GetFullPath((Join-Path $cwd $path))
    }
    $root = [System.IO.Path]::GetFullPath($cwd)
} catch {
    exit 0
}

$rel = $full
if ($full.StartsWith($root, [System.StringComparison]::OrdinalIgnoreCase)) {
    $rel = $full.Substring($root.Length)
}
$rel = $rel.TrimStart('\', '/') -replace '\\', '/'

# Never touch sqlc output; the PreToolUse guard already blocks edits to it.
if ($rel -match '^internal/db/([^/]+\.sql\.go|db\.go|models\.go|querier\.go)$') { exit 0 }

if (-not (Test-Path -LiteralPath $full -PathType Leaf)) { exit 0 }

$formatter = $null
$gofumpt = Get-Command gofumpt -CommandType Application -ErrorAction SilentlyContinue
if ($gofumpt) {
    $formatter = $gofumpt.Source
} else {
    $candidate = Join-Path $env:USERPROFILE 'go\bin\gofumpt.exe'
    if (Test-Path -LiteralPath $candidate -PathType Leaf) { $formatter = $candidate }
}

if (-not $formatter) {
    $gofmt = Get-Command gofmt -CommandType Application -ErrorAction SilentlyContinue
    if ($gofmt) { $formatter = $gofmt.Source }
}

if (-not $formatter) { exit 0 }

& $formatter -w $full
if ($LASTEXITCODE -ne 0) { exit 2 }

exit 0
