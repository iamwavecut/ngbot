package i18n

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/iamwavecut/ngbot/resources"
	"gopkg.in/yaml.v2"
)

func TestTranslationsKeysAreUsedAndComplete(t *testing.T) {
	t.Parallel()

	used, err := collectUsedI18nKeys()
	if err != nil {
		t.Fatalf("collect used i18n keys: %v", err)
	}

	defined, err := collectDefinedI18nKeys()
	if err != nil {
		t.Fatalf("collect defined i18n keys: %v", err)
	}

	missing := difference(used, defined)
	if len(missing) > 0 {
		t.Fatalf("missing translation keys:\n%s", strings.Join(missing, "\n"))
	}

	unused := difference(defined, used)
	if len(unused) > 0 {
		t.Fatalf("unused translation keys:\n%s", strings.Join(unused, "\n"))
	}
}

func TestAdminPanelTranslationsAreCompleteForSupportedLocales(t *testing.T) {
	t.Parallel()

	dict, err := loadTranslationsDict()
	if err != nil {
		t.Fatalf("load translations dict: %v", err)
	}

	keys := []string{
		"Gatekeeper Settings",
		"CAPTCHA Settings",
		"Greeting Settings",
		"LLM First Message",
		"Community Voting",
		"Spam Examples",
		"Selected",
		"Prompt examples cap: %d",
		"Voting timeout",
		"Min voters",
		"Max voters",
		"Min voters %",
		"Voting policy",
		"Insufficient votes on timeout => false-positive\nTie => wait one deciding vote",
		"Inherit",
		"No cap",
	}

	locales := make([]string, 0, len(languageNames)-1)
	for code := range languageNames {
		if strings.EqualFold(code, "en") {
			continue
		}
		locales = append(locales, strings.ToUpper(code))
	}
	sort.Strings(locales)

	for _, key := range keys {
		translations, ok := dict[key]
		if !ok {
			t.Fatalf("missing key in translations: %s", key)
		}
		for _, locale := range locales {
			value, ok := translations[locale]
			if !ok {
				t.Fatalf("missing locale %s for key %s", locale, key)
			}
			if strings.TrimSpace(value) == "" {
				t.Fatalf("empty translation for key %s locale %s", key, locale)
			}
		}
	}
}

func collectUsedI18nKeys() ([]string, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}

	internalDir := filepath.Join(root, "internal")
	fileSet := token.NewFileSet()
	keys := make(map[string]struct{})

	err = filepath.WalkDir(internalDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		node, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}

		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel == nil || selector.Sel.Name != "Get" {
				return true
			}
			pkgIdent, ok := selector.X.(*ast.Ident)
			if !ok || pkgIdent.Name != "i18n" {
				return true
			}
			if len(call.Args) < 1 {
				return true
			}
			value, ok := stringLiteralValue(call.Args[0])
			if !ok || value == "" {
				return true
			}
			keys[value] = struct{}{}
			return true
		})

		if strings.HasSuffix(filepath.ToSlash(path), "internal/handlers/chat/gatekeeper.go") {
			extractStringSliceVarLiterals(node, "challengeKeys", keys)
			extractStringSliceVarLiterals(node, "privateChallengeKeys", keys)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result, nil
}

func collectDefinedI18nKeys() ([]string, error) {
	dict, err := loadTranslationsDict()
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(dict))
	for key := range dict {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func loadTranslationsDict() (map[string]map[string]string, error) {
	content, err := resources.FS.ReadFile("i18n/translations.yml")
	if err != nil {
		return nil, err
	}
	dict := map[string]map[string]string{}
	if err := yaml.Unmarshal(content, &dict); err != nil {
		return nil, err
	}
	return dict, nil
}

func difference(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, item := range right {
		rightSet[item] = struct{}{}
	}
	diff := make([]string, 0)
	for _, item := range left {
		if _, ok := rightSet[item]; !ok {
			diff = append(diff, item)
		}
	}
	return diff
}

func stringLiteralValue(expr ast.Expr) (string, bool) {
	basic, ok := expr.(*ast.BasicLit)
	if !ok || basic.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(basic.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

func extractStringSliceVarLiterals(file *ast.File, varName string, out map[string]struct{}) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Names) != 1 || valueSpec.Names[0].Name != varName {
				continue
			}
			if len(valueSpec.Values) == 0 {
				continue
			}
			lit, ok := valueSpec.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elem := range lit.Elts {
				value, ok := stringLiteralValue(elem)
				if !ok || value == "" {
					continue
				}
				out[value] = struct{}{}
			}
		}
	}
}

func repoRoot() (string, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime caller is unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..")), nil
}
