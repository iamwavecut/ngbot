package config

import (
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

func TestDeploymentEnvironmentMatchesConfig(t *testing.T) {
	t.Parallel()

	composeData, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Fatalf("read tracked compose source: %v", err)
	}
	var compose struct {
		Services map[string]struct {
			Environment any `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(composeData, &compose); err != nil {
		t.Fatalf("decode compose source: %v", err)
	}
	service, ok := compose.Services["ngbot"]
	if !ok {
		t.Fatal("tracked compose source has no ngbot service")
	}
	composeNames := composeEnvironmentNames(t, service.Environment)

	exampleData, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatalf("read environment example: %v", err)
	}
	exampleNames := environmentExampleNames(string(exampleData))
	wantNames := configEnvironmentNames(reflect.TypeFor[Config](), "NG_")

	if missing := missingStrings(wantNames, composeNames); len(missing) != 0 {
		t.Fatalf("Compose is missing application variables: %v", missing)
	}
	if missing := missingStrings(wantNames, exampleNames); len(missing) != 0 {
		t.Fatalf(".env.example is missing application variables: %v", missing)
	}
	if unknown := unknownApplicationVariables(composeNames, wantNames); len(unknown) != 0 {
		t.Fatalf("Compose contains unknown NG_ variables: %v", unknown)
	}
	if unknown := unknownApplicationVariables(exampleNames, wantNames); len(unknown) != 0 {
		t.Fatalf(".env.example contains unknown NG_ variables: %v", unknown)
	}
}

func composeEnvironmentNames(t *testing.T, raw any) []string {
	t.Helper()

	var names []string
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			name, _, _ := strings.Cut(strings.TrimSpace(value.(string)), "=")
			names = append(names, name)
		}
	case map[any]any:
		for key := range values {
			names = append(names, key.(string))
		}
	default:
		t.Fatalf("unsupported Compose environment shape %T", raw)
	}
	sort.Strings(names)
	return names
}

func environmentExampleNames(data string) []string {
	var names []string
	for line := range strings.SplitSeq(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		if ok {
			names = append(names, strings.TrimSpace(name))
		}
	}
	sort.Strings(names)
	return names
}

func configEnvironmentNames(configType reflect.Type, prefix string) []string {
	var names []string
	for index := range configType.NumField() {
		field := configType.Field(index)
		tag := field.Tag.Get("env")
		if tag != "" {
			name, _, _ := strings.Cut(tag, ",")
			names = append(names, prefix+name)
			continue
		}
		if field.Type.Kind() == reflect.Struct {
			names = append(names, configEnvironmentNames(field.Type, prefix)...)
		}
	}
	sort.Strings(names)
	return names
}

func missingStrings(want, got []string) []string {
	var missing []string
	for _, value := range want {
		if !slices.Contains(got, value) {
			missing = append(missing, value)
		}
	}
	return missing
}

func unknownApplicationVariables(got, want []string) []string {
	var unknown []string
	for _, value := range got {
		if strings.HasPrefix(value, "NG_") && !slices.Contains(want, value) {
			unknown = append(unknown, value)
		}
	}
	return unknown
}
