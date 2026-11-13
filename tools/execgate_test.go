package tools

import "testing"

func TestDisciplineDenyHits(t *testing.T) {
	hits := []string{
		`type C:\src\main.go`,
		`cd x && type notes.txt`,
		`cat README.md`,
		`more < big.txt`,
		`head -n 20 x.txt`,
		`sed -n 10,20p f.go`,
		`timeout /t 30 /nobreak`,
		`Start-Sleep -Seconds 60`,
		`ping -n 5 127.0.0.1 >nul`,
		`(ping -n 3 localhost) & echo done`,
		`sleep 30`,
		`dir /s /b D:\proj`,
		`dir D:\x /s`,
		`for /r %f in (*.js) do @echo %f`,
		`where /r C:\ src *.cpp`,
		`findstr /s /i TODO *.go`,
	}
	for _, c := range hits {
		if disciplineDeny(c) == "" {
			t.Errorf("want blocked, allowed: %q", c)
		}
	}
}

func TestDisciplineDenyMisses(t *testing.T) {
	misses := []string{
		`git add -A && git commit -m x`,
		`go test ./...`,
		`netstat -ano | findstr :8080`,
		`findstr pattern file.txt`,
		`dir /b D:\proj`,
		`ping -n 3 192.168.1.10`,
		`git cat-file -p HEAD`,
		`pip install requests`,
		`node build.js`,
		`python -c "print(1)"`,
	}
	for _, c := range misses {
		if d := disciplineDeny(c); d != "" {
			t.Errorf("want allowed, blocked: %q (%s)", c, d)
		}
	}
}
