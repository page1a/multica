package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// settingsSurfaceExemptions are settings-family routes the generic entry
// deliberately does not call. A new route in the same family is not exempt
// until it is named here and in docs/kun/settings-cli-coverage.md.
var settingsSurfaceExemptions = map[string]string{
	"SetProjectVisibility":     "设置页「项目共享」用项目可见范围，最小通用入口未收",
	"PreviewProjectVisibility": "项目可见范围的预览，不是一项可读写的设置",
	"SetIssueVisibility":       "任务可见范围不在设置页，仍走任务接口",
}

var settingsRouteLine = regexp.MustCompile(`\.(Get|Post|Put|Patch|Delete)\("([^"]+)",\s*h\.([A-Za-z0-9]+)\)`)

func isSettingsSurface(path string) bool {
	switch {
	case strings.Contains(path, "/api/repos/"):
		return true
	case strings.Contains(path, "access-passes"):
		return true
	case strings.Contains(path, "runtime-skills/"):
		return true
	case path == "/api/modules" || strings.Contains(path, "/api/modules/"):
		return true
	case path == "/visibility" || path == "/visibility/preview":
		return true
	default:
		return false
	}
}

func settingsSurfaceFromRouter(src string) []string {
	var handlers []string
	seen := map[string]struct{}{}
	for _, match := range settingsRouteLine.FindAllStringSubmatch(src, -1) {
		path, handler := match[2], match[3]
		if !isSettingsSurface(path) {
			continue
		}
		if _, ok := seen[handler]; ok {
			continue
		}
		seen[handler] = struct{}{}
		handlers = append(handlers, handler)
	}
	return handlers
}

func checkSettingsSurface(routerSrc, doc string, claims map[string]struct{}) error {
	var missing []string
	for _, handler := range settingsSurfaceFromRouter(routerSrc) {
		if _, ok := claims[handler]; ok {
			continue
		}
		if _, exempt := settingsSurfaceExemptions[handler]; exempt {
			if !strings.Contains(doc, handler) {
				missing = append(missing, handler+" is exempted but missing from docs/kun/settings-cli-coverage.md")
			}
			continue
		}
		missing = append(missing, handler+" is a settings route and is not in the generic entry")
	}
	if len(missing) == 0 {
		return nil
	}
	return &settingsSurfaceError{missing: missing}
}

type settingsSurfaceError struct {
	missing []string
}

func (e *settingsSurfaceError) Error() string {
	return "settings surface drift:\n" + strings.Join(e.missing, "\n")
}

func TestSettingsSurfaceRoutesAreClaimed(t *testing.T) {
	routerSrc, doc := readSettingsSurfaceFiles(t)
	for handler := range settingsHandlerClaims() {
		if !strings.Contains(routerSrc, "h."+handler+")") {
			t.Errorf("claimed handler %s is not in router.go", handler)
		}
	}
	if err := checkSettingsSurface(routerSrc, doc, settingsHandlerClaims()); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsSurfaceFailsWhenAClaimIsRemoved(t *testing.T) {
	routerSrc, doc := readSettingsSurfaceFiles(t)
	claims := settingsHandlerClaims()
	delete(claims, "SetRepoVisibility")
	err := checkSettingsSurface(routerSrc, doc, claims)
	if err == nil {
		t.Fatal("dropping SetRepoVisibility coverage stayed green")
	}
	if !strings.Contains(err.Error(), "SetRepoVisibility") {
		t.Fatalf("error = %v, want it to name SetRepoVisibility", err)
	}
}

func TestSettingsSurfaceFailsWhenARouteIsAdded(t *testing.T) {
	routerSrc, doc := readSettingsSurfaceFiles(t)
	routerSrc += "\nr.Post(\"/api/repos/archive\", h.ArchiveRepo)\n"
	err := checkSettingsSurface(routerSrc, doc, settingsHandlerClaims())
	if err == nil {
		t.Fatal("a new /api/repos/ route stayed green")
	}
	if !strings.Contains(err.Error(), "ArchiveRepo") {
		t.Fatalf("error = %v, want it to name ArchiveRepo", err)
	}
}

func readSettingsSurfaceFiles(t *testing.T) (routerSrc, doc string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	dir := filepath.Dir(file)
	router, err := os.ReadFile(filepath.Join(dir, "../server/router.go"))
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := os.ReadFile(filepath.Join(dir, "../../../docs/kun/settings-cli-coverage.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(router), string(coverage)
}
