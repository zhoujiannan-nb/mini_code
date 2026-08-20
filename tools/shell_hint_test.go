package tools

import (
	"strings"
	"testing"
)

// TestWindowsCommandHintNewEntries covers the extended POSIX->cmd recovery
// map. Each entry must produce a hint naming the failed command and a
// suggestion; the hint is advisory text only.
func TestWindowsCommandHintNewEntries(t *testing.T) {
	cases := map[string]string{
		// command (as the model would write it) -> substring that must appear
		"find . -name '*.py'":  "find",
		"sleep 5":              "timeout /t",
		"touch out.txt":        "write_file",
		"sed -i 's/a/b/' f":    "sed",
		"awk '{print $1}' f":   "awk",
		"sudo apt-get install x": "Windows",
		"apt-get install foo":  "winget",
		"unzip a.zip":          "tar -xf",
		"zip -r a.zip d/":      "tar -a -cf",
		"less f.txt":           "more",
		"ifconfig":             "ipconfig",
		"ip addr":              "ipconfig",
		"ss -ltnp":             "netstat -ano",
		"lsof -i :8080":        "netstat -ano",
		"stat f.txt":           "dir",
		"du -sh .":             "dir",
		"wc -l f.txt":          "wc",
		"env":                  "set",
		"export FOO=bar":       "set",
		"source .env":          "call",
		"chown user f":         "icacls",
		"uname -a":             "ver",
		"id":                   "whoami",
		"man grep":             "--help",
		"clear":                "cls",
		"killall python":       "taskkill /IM",
		"pkill -f x":           "taskkill /FI",
		"traceroute example.com": "tracert",
		"dig example.com":      "nslookup",
		"hexdump -C f":         "certutil -encodehex",
		"sha256sum f":          "certutil -hashfile SHA256",
		"md5sum f":             "certutil -hashfile MD5",
		"systemctl status x":   "sc",
		"journalctl -xe":       "eventvwr",
		"dmesg":                "eventvwr",
		"crontab -l":           "schtasks",
		"sh script.sh":         "cmd",
		"bash script.sh":       "cmd",
		"make":                 "compiler",
		"gcc main.c":           "MinGW",
		"ping -c 3 127.0.0.1":  "ping -n",
		"date +%Y-%m-%d":       "python",
		"sort -u f":            "sort",
		"cut -d, -f1 f":        "cut",
		"for f in *.py; do echo $f; done": "for /f",
		"if [ -f x ]; then echo y; fi":    "if exist",
		"while read line; do echo $line; done": "script",
		"case $x in a) echo a;; esac": "if",
		"read -r line":         "set /p",
		"printf '%s\\n' x":     "echo",
		"history":              "doskey",
		"alias ll='ls -la'":    "doskey",
		"top":                  "taskmgr",
		"htop":                 "taskmgr",
		"lscpu":                "systeminfo",
		"file f.bin":           "script",
		"seq 1 10":             "script",
		"nohup long.sh &":      "start",
		"time python x.py":     "script",
		"watch -n 1 df":        "watch",
		"eval '$(echo hi)'":    "eval",
		"exec python x.py":     "exec",
		"trap 'echo bye' EXIT": "trap",
		"ninja":                "ninja-build",
		"g++ main.cpp":         "MinGW",
		"cc main.c":            "MinGW",
		"dpkg -l":              "winget",
		"rpm -qa":              "winget",
		"brew install foo":     "winget",
		"snap install foo":     "winget",
		"flatpak install foo":  "winget",
		"yum install foo":      "winget",
		"dnf install foo":      "winget",
		"pacman -S foo":        "winget",
		"zypper install foo":   "winget",
		"apt install foo":      "winget",
		"su root":              "Windows",
		"doas ls":              "Windows",
		"chgrp group f":        "chgrp",
		"readlink f":           "readlink",
		"umount /mnt":          "umount",
		"groups":               "groups",
		"service x status":     "sc",
		"at 12:00 cmd":         "schtasks",
		"whereis git":          "where",
		"base64 f":             "certutil",
		"od -c f":              "script",
		"uniq f":               "script",
		"tee log.txt":          "script",
		"xargs -I{} echo {}":   "script",
		"realpath f":           "script",
		"basename f.txt .txt":  "script",
		"dirname f.txt":        "script",
		"mktemp":               "script",
		"df -h":                "script",
		"free -m":              "script",
		"shuf f":               "script",
		"yes | head -5":        "yes",
		"fish":                 "fish",
		"dash":                 "dash",
		"ksh":                  "ksh",
		"csh":                  "csh",
		"local x=1":            "local",
		"return 0":             "return",
		"function f() { :; }":  "function",
		"command -v git":       "command",
		"builtin cd":           "builtin",
		"curl http://x":        "Invoke-WebRequest",
		"tar -czf a.tgz d":     "Windows 10+",
	}
	for cmd, wantSub := range cases {
		got := windowsCommandHint(cmd)
		if !strings.Contains(got, wantSub) {
			t.Errorf("windowsCommandHint(%q) = %q, want it to contain %q", cmd, got, wantSub)
		}
	}
}

// TestWindowsCommandHintLegacyEntries guards the pre-existing map entries.
func TestWindowsCommandHintLegacyEntries(t *testing.T) {
	cases := map[string]string{
		"ls -la":       "dir",
		"cat f.txt":    "type",
		"grep foo f":   "findstr",
		"rm -i f":      "del",
		"cp a b":       "copy",
		"mv a b":       "move",
		"chmod 755 f":  "icacls",
		"which python": "where",
		"pwd":          "cd",
		"head -n 5 f":  "more",
		"tail -n 5 f":  "more",
		"ps aux":       "tasklist",
		"kill 123":     "taskkill",
		"python3 x.py": "python",
		"pip3 install x": "pip",
		"wget http://x": "curl",
		"diff a b":     "fc",
	}
	for cmd, wantSub := range cases {
		got := windowsCommandHint(cmd)
		if !strings.Contains(got, wantSub) {
			t.Errorf("windowsCommandHint(%q) = %q, want it to contain %q", cmd, got, wantSub)
		}
	}
}

// TestWindowsCommandHintNoFalsePositives: unknown commands and plain cmd.exe
// commands must not produce a hint.
func TestWindowsCommandHintNoFalsePositives(t *testing.T) {
	for _, cmd := range []string{
		"",
		"dir /b",
		"echo hello",
		"python x.py",
		"git status",
		"node app.js",
		"tasklist",
		"ipconfig /all",
		"findstr foo f.txt",
		"copy a b",
		"mkdir a\\b\\c",
		"del /q f.txt",
		"timeout /t 2",
		"certutil -hashfile f SHA256",
		"winget install foo",
		"schtasks /query",
		"nslookup example.com",
		"tracert example.com",
		"systeminfo",
		"ver",
		"whoami",
		"cls",
		"set PATH",
		"call sub.bat",
		"doskey",
		"mklink",
		"subst",
		"chkdsk",
		"taskmgr",
		"eventvwr",
		"powershell -c 1",
		"Invoke-WebRequest",
		"robocopy a b /mir",
		"sc query",
	} {
		if got := windowsCommandHint(cmd); got != "" {
			t.Errorf("windowsCommandHint(%q) = %q, want empty (no false positive)", cmd, got)
		}
	}
}

// TestWindowsCommandHintSegmentsAndDedupe: hints fire per && / || / | / ;
// segment, and a repeated command is mentioned only once.
func TestWindowsCommandHintSegmentsAndDedupe(t *testing.T) {
	got := windowsCommandHint("ls && cat f.txt")
	if !strings.Contains(got, "ls") || !strings.Contains(got, "cat") {
		t.Errorf("expected both ls and cat hints, got: %s", got)
	}

	got = windowsCommandHint("ls; ls; ls")
	if n := strings.Count(got, "`ls`"); n != 1 {
		t.Errorf("expected exactly one ls hint, got %d in: %s", n, got)
	}

	// Env-var assignment prefix: first token is skipped (existing behavior).
	if got := windowsCommandHint("FOO=bar ls"); got != "" {
		t.Errorf("expected no hint for env-assignment prefix, got: %s", got)
	}

	// Case-insensitive matching.
	if got := windowsCommandHint("LS -LA"); !strings.Contains(got, "ls") {
		t.Errorf("expected case-insensitive ls hint, got: %s", got)
	}
}

// TestMkdirCmdNote covers the mkdir special case (verified empirically on
// Windows: "mkdir -p x" exits 0 but creates a junk "-p x" directory;
// "mkdir a/b" fails because the unquoted '/' is parsed as a switch).
func TestMkdirCmdNote(t *testing.T) {
	cases := []struct {
		cmd      string
		wantP    bool // note mentions the -p flag
		wantSlash bool // note mentions the unquoted '/'
	}{
		{"mkdir -p a/b", true, true},
		{"mkdir -p a", true, false},
		{"mkdir a/b", false, true},
		{"mkdir a/b/c", false, true},
		{"mkdir \"a/b\"", false, true}, // advisory only; still worth the nudge
		{"mkdir a\\b\\c", false, false},
		{"mkdir a", false, false},
		{"echo mkdir -p x", false, false}, // not a command position... (no \b before? 'mkdir' is at word boundary)
	}
	for _, c := range cases {
		got := mkdirCmdNote(c.cmd)
		hasP := strings.Contains(got, "-p")
		hasSlash := strings.Contains(got, "'/'")
		if hasP != c.wantP {
			t.Errorf("mkdirCmdNote(%q) -p mention = %v, want %v (note: %s)", c.cmd, hasP, c.wantP, got)
		}
		if hasSlash != c.wantSlash {
			t.Errorf("mkdirCmdNote(%q) slash mention = %v, want %v (note: %s)", c.cmd, hasSlash, c.wantSlash, got)
		}
	}
}

// TestMkdirCmdNoteNotEmbedded: the note must only fire when mkdir is the
// actual command of a segment, not when the word appears inside another
// command's arguments.
func TestMkdirCmdNoteNotEmbedded(t *testing.T) {
	for _, cmd := range []string{
		"echo mkdir -p x",
		"dir /b",
		"findstr mkdir f.txt",
		"python -c \"print('mkdir')\"",
	} {
		if got := mkdirCmdNote(cmd); got != "" {
			t.Errorf("mkdirCmdNote(%q) = %q, want empty (not a mkdir command)", cmd, got)
		}
	}
	// But a chained segment still triggers.
	if got := mkdirCmdNote("ls && mkdir -p a/b"); got == "" {
		t.Error("expected note for mkdir in a chained segment")
	}
}
