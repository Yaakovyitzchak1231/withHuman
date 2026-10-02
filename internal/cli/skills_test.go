package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The published skills (skills/*/SKILL.md, installed with `npx skills
// add`) teach agents this CLI. These tests keep them installable and keep
// every command they show one the CLI accepts.

var (
	skillName        = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	skillCommandUse  = regexp.MustCompile("\\b(?:withhuman|sh -s --) ([a-z][a-z-]*)(?: ([a-z][a-z-]*))?(?:[\\s,.`]|$)")
	skillFrontmatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)
)

func skillFiles(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "skills", "*", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no skills found under skills/")
	}
	files := make(map[string]string, len(paths))
	for _, path := range paths {
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files[path] = string(text)
	}
	return files
}

func TestSkillsHaveInstallableFrontmatter(t *testing.T) {
	for path, text := range skillFiles(t) {
		match := skillFrontmatter.FindStringSubmatch(text)
		if match == nil {
			t.Errorf("%s: no frontmatter", path)
			continue
		}
		fields := map[string]string{}
		for _, line := range strings.Split(match[1], "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if ok {
				fields[key] = strings.TrimSpace(value)
			}
		}
		dir := filepath.Base(filepath.Dir(path))
		if name := fields["name"]; name != dir || len(name) > 64 || !skillName.MatchString(name) {
			t.Errorf("%s: name %q must equal its directory and be lowercase words joined by hyphens", path, name)
		}
		if description := fields["description"]; description == "" || len(description) > 1024 {
			t.Errorf("%s: description must be 1 to 1024 characters, got %d", path, len(description))
		}
		if strings.Contains(text, "—") {
			t.Errorf("%s: user-facing text must not use em dashes", path)
		}
	}
}

func TestSkillsShowOnlyPublicCommands(t *testing.T) {
	for path, text := range skillFiles(t) {
		for _, match := range skillCommandUse.FindAllStringSubmatch(text, -1) {
			command := match[1]
			if command == "agent" {
				command += " " + match[2]
			}
			if !strings.Contains(usageText, "withhuman "+command+" ") && !strings.Contains(usageText, "withhuman "+command+"\n") {
				t.Errorf("%s: %q is not a public command of the CLI", path, match[0])
			}
		}
	}
}
