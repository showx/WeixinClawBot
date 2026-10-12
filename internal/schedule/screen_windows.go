//go:build windows

package schedule

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func readOpenPage(ctx context.Context, exe, rawURL string) (string, error) {
	dir, err := os.MkdirTemp("", "wxclaw-screen-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	scriptPath := filepath.Join(dir, "read.ps1")
	if err := os.WriteFile(scriptPath, append([]byte{0xEF, 0xBB, 0xBF}, []byte(screenScript)...), 0o600); err != nil {
		return "", err
	}
	outPath := filepath.Join(dir, "out.txt")
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", scriptPath, "-Exe", exe, "-Url", rawURL, "-Out", outPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 180 {
			msg = msg[len(msg)-180:]
		}
		if msg != "" {
			return "", errors.New("没有从已登录的浏览器读出页面：" + msg)
		}
		return "", errors.New("没有从已登录的浏览器读出页面")
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		return "", errors.New("没有从已登录的浏览器读出页面")
	}
	text := strings.TrimSpace(strings.TrimPrefix(string(data), "\uFEFF"))
	if utf8.RuneCountInString(text) < 8 {
		note, _ := os.ReadFile(outPath + ".log")
		msg := strings.TrimSpace(string(note))
		if len(msg) > 240 {
			msg = msg[len(msg)-240:]
		}
		if msg != "" {
			return "", errors.New("页面没有读出文字：" + msg)
		}
		return "", errors.New("页面没有读出文字")
	}
	return text, nil
}

const screenScript = `
param(
  [string]$Exe,
  [string]$Url,
  [string]$Out
)
$ErrorActionPreference = "Stop"
Add-Type -AssemblyName UIAutomationClient
Add-Type @"
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public class WxRead {
  public delegate bool EnumProc(IntPtr hWnd, IntPtr lParam);
  public static string ProcName = "chrome";
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc lpEnumFunc, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool EnumChildWindows(IntPtr hWnd, EnumProc lpEnumFunc, IntPtr lParam);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint pid);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetClassName(IntPtr hWnd, StringBuilder lpClassName, int nMaxCount);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr hWnd, StringBuilder lpString, int nMaxCount);
  [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr hWnd, uint msg, IntPtr wParam, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr hWnd, IntPtr hWndInsertAfter, int X, int Y, int cx, int cy, uint uFlags);
  public static List<IntPtr> Windows() {
    var pids = new HashSet<uint>();
    foreach (var p in System.Diagnostics.Process.GetProcessesByName(ProcName)) pids.Add((uint)p.Id);
    var found = new List<IntPtr>();
    EnumWindows((h, l) => {
      uint pid = 0;
      GetWindowThreadProcessId(h, out pid);
      if (!pids.Contains(pid) || !IsWindowVisible(h)) return true;
      var cls = new StringBuilder(256);
      GetClassName(h, cls, cls.Capacity);
      if (cls.ToString() == "Chrome_WidgetWin_1") found.Add(h);
      return true;
    }, IntPtr.Zero);
    return found;
  }
  public static string Title(IntPtr h) {
    var sb = new StringBuilder(512);
    GetWindowText(h, sb, sb.Capacity);
    return sb.ToString();
  }
  public static string ClassName(IntPtr h) {
    var sb = new StringBuilder(256);
    GetClassName(h, sb, sb.Capacity);
    return sb.ToString();
  }
  public static List<IntPtr> Children(IntPtr parent, string className) {
    var found = new List<IntPtr>();
    EnumChildWindows(parent, (h, l) => {
      var cls = new StringBuilder(256);
      GetClassName(h, cls, cls.Capacity);
      if (cls.ToString() == className) found.Add(h);
      return true;
    }, IntPtr.Zero);
    return found;
  }
}
"@
[WxRead]::ProcName = [IO.Path]::GetFileNameWithoutExtension($Exe)
$before = @{}
[WxRead]::Windows() | ForEach-Object { $before[$_.ToInt64()] = $true }
Start-Process -FilePath $Exe -ArgumentList @("--new-window", "--window-position=60,40", "--window-size=1400,900", $Url)
$fresh = [IntPtr]::Zero
$blank = -join (@(0x65E0,0x6807,0x9898) | ForEach-Object { [char]$_ })
$deadline = (Get-Date).AddSeconds(15)
while ((Get-Date) -lt $deadline) {
  Start-Sleep -Milliseconds 400
  foreach ($hwnd in [WxRead]::Windows()) {
    if (-not $before.ContainsKey($hwnd.ToInt64())) { $fresh = $hwnd }
  }
  if ($fresh -ne [IntPtr]::Zero) {
    $title = [WxRead]::Title($fresh)
    if ($title -and -not $title.Contains($blank)) { break }
  }
}
if ($fresh -eq [IntPtr]::Zero) { exit 1 }
$best = ""
$last = ""
$quietSince = $null
$started = Get-Date
$deadline = (Get-Date).AddSeconds(32)
$trueCond = [System.Windows.Automation.Condition]::TrueCondition
$loadingWords = @("加载中", "正在加载", "正在等待", "数据加载中")
try {
  while ((Get-Date) -lt $deadline) {
    $round = ""
    $busy = $false
    try {
      $renders = [WxRead]::Children($fresh, "Chrome_RenderWidgetHostHWND")
      foreach ($render in $renders) {
        $widget = [System.Windows.Automation.AutomationElement]::FromHandle($render)
        if (-not $widget) { continue }
        $nodes = $widget.FindAll([System.Windows.Automation.TreeScope]::Descendants, $trueCond)
        $parts = New-Object System.Collections.Generic.List[string]
        foreach ($node in $nodes) {
          try {
            $ctype = ""
            if ($node.Current.ControlType) { $ctype = $node.Current.ControlType.ProgrammaticName }
            if ($ctype -eq "ControlType.ProgressBar") { $busy = $true }
            $name = $node.Current.Name
            if (-not $name) { continue }
            $trim = $name.Trim()
            if ($trim.Length -eq 0) { continue }
            foreach ($word in $loadingWords) {
              if ($trim.Contains($word)) { $busy = $true }
            }
            $parts.Add($trim)
          } catch {}
        }
        $text = ($parts -join " ")
        if ($text.Length -gt $round.Length) { $round = $text }
      }
    } catch {
      $quietSince = $null
      Start-Sleep -Milliseconds 500
      continue
    }
    if ($round.Length -gt $best.Length) { $best = $round }
    $elapsed = ((Get-Date) - $started).TotalSeconds
    $settled = (-not $busy) -and ($round.Length -gt 80) -and ($round -eq $last)
    if ($settled) {
      if (-not $quietSince) { $quietSince = Get-Date }
      $quiet = ((Get-Date) - $quietSince).TotalSeconds
      if ($elapsed -ge 12 -and $quiet -ge 5) { break }
    } else {
      $quietSince = $null
    }
    $last = $round
    Start-Sleep -Milliseconds 500
  }
  $utf8 = New-Object System.Text.UTF8Encoding $false
  [System.IO.File]::WriteAllText($Out, $best, $utf8)
} finally {
  if ($fresh -ne [IntPtr]::Zero) {
    [WxRead]::PostMessage($fresh, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero) | Out-Null
  }
}
`
