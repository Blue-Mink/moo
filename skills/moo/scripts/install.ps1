<#
.SYNOPSIS
  通用技能跨 Agent 安装器（Windows PowerShell 5.1+，默认 dry-run）
.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Target claude-code -Apply
  powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -List
#>
[CmdletBinding()]
param(
    [string]$Target,
    [string]$Dest,
    [string[]]$Dep = @(),
    [switch]$Link,
    [switch]$Apply,
    [switch]$List
)

$ErrorActionPreference = 'Stop'
$SkillDir = Split-Path -Parent $PSScriptRoot
$Name = Split-Path -Leaf $SkillDir

$Targets = @{
    'qwenpaw'      = @{ Kind = 'dir';     Def = (Join-Path (Get-Location) 'skills') }
    'claude-code'  = @{ Kind = 'dir';     Def = (Join-Path $HOME '.claude\skills') }
    'qwen-code'    = @{ Kind = 'dir';     Def = (Join-Path (Get-Location) '.qwen\skills') }
    'agents-skills'= @{ Kind = 'dir';     Def = (Join-Path (Get-Location) '.agents\skills') }
    'skills-dir'   = @{ Kind = 'dir';     Def = '' }
    'codex'        = @{ Kind = 'prompt';  Def = (Join-Path $HOME '.codex\prompts') }
    'gemini-cli'   = @{ Kind = 'prompt';  Def = (Join-Path $HOME '.gemini\commands') }
    'hermes'       = @{ Kind = 'prompt';  Def = '' }
    'generic'      = @{ Kind = 'prompt';  Def = '' }
    'agents-md'    = @{ Kind = 'section'; Def = (Get-Location).Path; File = 'AGENTS.md' }
    'cursor'       = @{ Kind = 'rule';    Def = (Join-Path (Get-Location) '.cursor\rules'); Fe = 'alwaysApply: true'; Ext = '.mdc' }
    'windsurf'     = @{ Kind = 'rule';    Def = (Join-Path (Get-Location) '.windsurf\rules'); Fe = 'trigger: always_on'; Ext = '.md' }
    'cline'        = @{ Kind = 'rule';    Def = (Join-Path (Get-Location) '.clinerules'); Ext = '.md' }
    'roo'         = @{ Kind = 'rule';    Def = (Join-Path (Get-Location) '.roo\rules'); Ext = '.md' }
    'kilo'        = @{ Kind = 'rule';    Def = (Join-Path (Get-Location) '.kilo\rules'); Ext = '.md' }
    'continue'    = @{ Kind = 'rule';    Def = (Join-Path (Get-Location) '.continue\rules'); Ext = '.md' }
    'augment'     = @{ Kind = 'rule';    Def = (Join-Path (Get-Location) '.augment\rules'); Ext = '.md' }
    'openhands'   = @{ Kind = 'rule';    Def = (Join-Path (Get-Location) '.openhands\microagents'); Ext = '.md' }
    'trae'        = @{ Kind = 'section'; Def = (Get-Location).Path; File = '.trae\rules\project_rules.md' }
    'junie'       = @{ Kind = 'section'; Def = (Get-Location).Path; File = '.junie\guidelines.md' }
    'aider'       = @{ Kind = 'section'; Def = (Get-Location).Path; File = 'CONVENTIONS.md' }
    'warp'        = @{ Kind = 'section'; Def = (Get-Location).Path; File = 'WARP.md' }
    'copilot'      = @{ Kind = 'rule';    Def = (Join-Path (Get-Location) '.github\instructions'); Fe = 'applyTo: "**"'; Ext = '.instructions.md' }
    'kiro'         = @{ Kind = 'rule';    Def = (Join-Path (Get-Location) '.kiro\steering'); Fe = 'inclusion: always'; Ext = '.md' }
    'all-project'  = @{ Kind = 'multi';   Def = (Get-Location).Path }
}

if ($List) {
    Write-Host '技能目录 : qwenpaw claude-code qwen-code agents-skills skills-dir'
    Write-Host '提示词   : codex gemini-cli hermes generic'
    Write-Host '项目规则 : agents-md cursor windsurf cline roo kilo continue augment copilot kiro openhands trae junie aider warp'
    Write-Host '组合     : all-project'
    exit 0
}
if (-not $Target) { Write-Host '用法: install.ps1 -Target <名称> [-Dest 目录] [-Dep 技能] [-Link] [-Apply] [-List]'; exit 1 }
if (-not $Targets.ContainsKey($Target)) { Write-Host "不支持的目标: $Target"; exit 1 }

$cfg = $Targets[$Target]
$kind = $cfg.Kind
if (-not $Dest) { $Dest = $cfg.Def }
if (-not $Dest -and $kind -ne 'multi') { Write-Host "target=$Target 需要 -Dest"; exit 1 }

$desc = ''
$skillMd = Join-Path $SkillDir 'SKILL.md'
if (Test-Path $skillMd) {
    $m = Select-String -Path $skillMd -Pattern '^description:\s*(.+)$' | Select-Object -First 1
    if ($m) { $desc = $m.Matches[0].Groups[1].Value.Trim('"') }
}

Write-Host "技能     : $Name"
Write-Host "目录     : $SkillDir"
Write-Host "目标     : $Target"
if ($kind -ne 'multi') { Write-Host "落地     : $Dest" }
if ($Dep.Count -gt 0) { Write-Host "依赖     : $($Dep -join ', ')" }
Write-Host ("模式     : " + $(if ($Apply) { '执行' } else { '预演-dry-run' }))

function Assert-Outside {
    param([string]$Path)
    $abs = [System.IO.Path]::GetFullPath($Path)
    if ($abs -eq $SkillDir -or $abs.StartsWith($SkillDir + [System.IO.Path]::DirectorySeparatorChar)) {
        Write-Error "目标落在技能自身内（$abs）"; exit 1
    }
}
if ($kind -ne 'prompt' -and $Dest) { Assert-Outside $Dest }

function Copy-Tree {
    param([string]$Src, [string]$Dst)
    if (Test-Path $Dst) { Remove-Item -LiteralPath $Dst -Recurse -Force }
    New-Item -ItemType Directory -Path $Dst -Force | Out-Null
    Get-ChildItem -LiteralPath $Src -Force | Where-Object {
        $_.Name -notmatch '^(\.git|\.pushpack.*|__pycache__|node_modules|\.DS_Store)$' -and $_.Name -notlike '*.pyc'
    } | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination $Dst -Recurse -Force
    }
}

function Install-One {
    param([string]$Src, [string]$Nm)
    if ($Link) {
        Write-Host "  + 链接 $Dest\$Nm"
        if ($Apply) {
            $p = Join-Path $Dest $Nm
            if (Test-Path $p) { Remove-Item -LiteralPath $p -Recurse -Force }
            New-Item -ItemType SymbolicLink -Path $p -Target $Src | Out-Null
        }
    } else {
        Write-Host "  + 复制 $Dest\$Nm"
        if ($Apply) { Copy-Tree $Src (Join-Path $Dest $Nm) }
    }
}

function Install-Deps {
    foreach ($d in $Dep) {
        $depDir = Join-Path (Split-Path -Parent $SkillDir) $d
        if (Test-Path $depDir) { Install-One $depDir $d }
        else { Write-Host "  = 未找到依赖技能 $d（跳过）" }
    }
}

function Write-Prompt {
    New-Item -ItemType Directory -Path $Dest -Force | Out-Null
    $tpl = ''
    if ($Target -eq 'codex') { $tpl = Join-Path $SkillDir 'adapters\codex\prompt.md' }
    elseif ($Target -in @('hermes', 'generic')) {
        $tpl = Join-Path $SkillDir 'adapters\hermes\prompt.md'
        if (-not (Test-Path $tpl)) { $tpl = Join-Path $SkillDir 'adapters\generic\PROMPT.md' }
    }
    if ($tpl -and (Test-Path $tpl)) {
        $out = Join-Path $Dest "$Name.md"
        Write-Host "  + 生成 $out"
        if ($Apply) { (Get-Content -Raw $tpl).Replace('{{SKILL_DIR}}', $SkillDir) | Set-Content -Encoding UTF8 $out }
        return
    }
    if ($Target -eq 'gemini-cli') {
        $out = Join-Path $Dest "$Name.toml"
        Write-Host "  + 生成 $out"
        if ($Apply) {
            $body = "description = `"$desc`"`nprompt = '''`n读取 $SkillDir/SKILL.md 并严格按其流程执行。`n`n$desc`n'''"
            Set-Content -Encoding UTF8 $out $body
        }
        return
    }
    $out = Join-Path $Dest "$Name.md"
    Write-Host "  + 生成 $out"
    if ($Apply) {
        $body = "---`nname: $Name`ndescription: `"$desc`"`n---`n`n开始前先完整读取 ``$SkillDir/SKILL.md``，按其中的流程与硬规则执行。`n需要用户决定的地方给编号选项（1/2/3）；写操作前确认。`n"
        Set-Content -Encoding UTF8 $out $body
    }
}

function Write-Rule {
    $ext = if ($cfg.Ext) { $cfg.Ext } else { '.md' }
    $fe = if ($cfg.Fe) { $cfg.Fe } else { '' }
    New-Item -ItemType Directory -Path $Dest -Force | Out-Null
    $out = Join-Path $Dest ($Name + $ext)
    Write-Host "  + 生成 $out"
    if ($Apply) {
        $body = "---`ndescription: `"$Name`"`n$fe`n---`n`n# $Name 技能`n`n$desc`n`n任务涉及该领域时，读取 ``$SkillDir/SKILL.md`` 并遵循。`n写操作前向用户确认；需要选择时给编号选项。`n"
        Set-Content -Encoding UTF8 $out $body
    }
}

function Add-Section {
    $f = Join-Path $Dest $cfg.File
    New-Item -ItemType Directory -Path (Split-Path -Parent $f) -Force | Out-Null
    if ((Test-Path $f) -and (Select-String -Path $f -Pattern "$Name`:begin" -Quiet)) {
        Write-Host "  = $f 已有本技能段落，跳过"; return
    }
    Write-Host "  + 追加技能段落到 $f"
    if ($Apply) {
        $block = "`n<!-- $Name`:begin -->`n## 技能：$Name`n`n- **$Name**：``$SkillDir/SKILL.md```n`n任务涉及该领域时先读 SKILL.md。`n<!-- $Name`:end -->`n"
        Add-Content -Encoding UTF8 $f $block
    }
}

switch ($kind) {
    'dir' {
        New-Item -ItemType Directory -Path $Dest -Force | Out-Null
        Install-One $SkillDir $Name
        if ($Name -eq 'fpk-build-to-moo' -and -not $Link) {
            $root = Split-Path -Parent (Split-Path -Parent $SkillDir)
            $gen = Join-Path $root 'tools\gen-repo-moo-json.py'
            if (Test-Path $gen) {
                Write-Host "  + 附带生成器与协议文档 → $Dest\$Name\bundled\"
                if ($Apply) {
                    New-Item -ItemType Directory -Path (Join-Path $Dest "$Name\bundled\tools") -Force | Out-Null
                    New-Item -ItemType Directory -Path (Join-Path $Dest "$Name\bundled\docs") -Force | Out-Null
                    Copy-Item (Join-Path $root 'tools\gen-moo-json') (Join-Path $Dest "$Name\bundled\tools") -Recurse -Force -ErrorAction SilentlyContinue
                    Copy-Item $gen (Join-Path $Dest "$Name\bundled\tools") -Force -ErrorAction SilentlyContinue
                    Copy-Item (Join-Path $root 'docs\Moo应用源协议.md') (Join-Path $Dest "$Name\bundled\docs") -Force -ErrorAction SilentlyContinue
                }
            }
        }
        Install-Deps
    }
    'prompt'  { Write-Prompt }
    'rule'    { Write-Rule }
    'section' { Add-Section }
    'multi' {
        Write-Host '  组合安装（写入当前项目）'
        foreach ($t in @('agents-md', 'cursor', 'windsurf', 'cline', 'continue', 'copilot', 'kiro')) {
            $childArgs = @('-Target', $t)
            if ($Apply) { $childArgs += '-Apply' }
            & $PSCommandPath @childArgs
        }
    }
}

Write-Host ''
if ($Apply) { Write-Host '完成。' } else { Write-Host '预演结束（未写入任何文件）。加 -Apply 执行。' }
