package db

import "strings"

const GatekeeperGreetingTemplateMarkdownV2Prefix = "mdv2:"

func IsGatekeeperGreetingMarkdownV2Template(template string) bool {
	return strings.HasPrefix(template, GatekeeperGreetingTemplateMarkdownV2Prefix)
}

func StripGatekeeperGreetingTemplateSyntax(template string) string {
	return strings.TrimPrefix(template, GatekeeperGreetingTemplateMarkdownV2Prefix)
}

func WrapGatekeeperGreetingMarkdownV2Template(template string) string {
	return GatekeeperGreetingTemplateMarkdownV2Prefix + template
}
