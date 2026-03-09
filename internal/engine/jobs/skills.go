package jobs

import (
	"regexp"
	"sort"
	"strings"
)

// skillPatterns maps canonical skill names to their regex patterns.
var skillPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	// Languages
	{"Go", regexp.MustCompile(`\b(?:Go|Golang|golang)\b`)},
	{"Rust", regexp.MustCompile(`\bRust\b`)},
	{"Python", regexp.MustCompile(`\bPython\b`)},
	{"TypeScript", regexp.MustCompile(`\bTypeScript\b`)},
	{"JavaScript", regexp.MustCompile(`\bJavaScript\b`)},
	{"Scala", regexp.MustCompile(`\bScala\b`)},
	{"Java", regexp.MustCompile(`\bJava\b`)},
	{"C++", regexp.MustCompile(`\bC\+\+\b`)},
	{"C#", regexp.MustCompile(`\bC#\b`)},
	{"Ruby", regexp.MustCompile(`\bRuby\b`)},
	{"PHP", regexp.MustCompile(`\bPHP\b`)},
	{"Swift", regexp.MustCompile(`\bSwift\b`)},
	{"Kotlin", regexp.MustCompile(`\bKotlin\b`)},
	{"Elixir", regexp.MustCompile(`\bElixir\b`)},
	{"Haskell", regexp.MustCompile(`\bHaskell\b`)},
	{"Zig", regexp.MustCompile(`\bZig\b`)},
	{"C", regexp.MustCompile(`\bC\b`)},
	{"Lua", regexp.MustCompile(`\bLua\b`)},
	// Frameworks & Libraries
	{"React", regexp.MustCompile(`\bReact\b`)},
	{"Vue", regexp.MustCompile(`\bVue\.?js?\b`)},
	{"Angular", regexp.MustCompile(`\bAngular\b`)},
	{"Next.js", regexp.MustCompile(`\bNext\.?js\b`)},
	{"Node.js", regexp.MustCompile(`\bNode\.?js\b`)},
	{"Django", regexp.MustCompile(`\bDjango\b`)},
	{"Flask", regexp.MustCompile(`\bFlask\b`)},
	{"FastAPI", regexp.MustCompile(`\bFastAPI\b`)},
	{"Spring", regexp.MustCompile(`\bSpring\b`)},
	{"Rails", regexp.MustCompile(`\bRails\b`)},
	{"ZIO", regexp.MustCompile(`\bZIO\b`)},
	{"Svelte", regexp.MustCompile(`\bSvelte\b`)},
	{"Tailwind", regexp.MustCompile(`\bTailwind\b`)},
	// Databases
	{"PostgreSQL", regexp.MustCompile(`\b(?:PostgreSQL|Postgres)\b`)},
	{"MySQL", regexp.MustCompile(`\bMySQL\b`)},
	{"MongoDB", regexp.MustCompile(`\bMongo(?:DB)?\b`)},
	{"Redis", regexp.MustCompile(`\bRedis\b`)},
	{"SQLite", regexp.MustCompile(`\bSQLite\b`)},
	{"Qdrant", regexp.MustCompile(`\bQdrant\b`)},
	{"Elasticsearch", regexp.MustCompile(`\bElasticsearch\b`)},
	// Infrastructure & Tools
	{"Docker", regexp.MustCompile(`\bDocker\b`)},
	{"Kubernetes", regexp.MustCompile(`\b(?:Kubernetes|K8s)\b`)},
	{"AWS", regexp.MustCompile(`\bAWS\b`)},
	{"GCP", regexp.MustCompile(`\bGCP\b`)},
	{"Azure", regexp.MustCompile(`\bAzure\b`)},
	{"Terraform", regexp.MustCompile(`\bTerraform\b`)},
	{"CI/CD", regexp.MustCompile(`\bCI/?CD\b`)},
	{"GraphQL", regexp.MustCompile(`\bGraphQL\b`)},
	{"gRPC", regexp.MustCompile(`\bgRPC\b`)},
	{"REST", regexp.MustCompile(`\bREST(?:ful)?\b`)},
	{"Linux", regexp.MustCompile(`\bLinux\b`)},
	{"Wayland", regexp.MustCompile(`\bWayland\b`)},
	{"XDG", regexp.MustCompile(`\bXDG\b`)},
	{"D-Bus", regexp.MustCompile(`\bD-Bus\b`)},
	{"CMake", regexp.MustCompile(`\bCMake\b`)},
	{"Qt", regexp.MustCompile(`\bQt\b`)},
	{"GTK", regexp.MustCompile(`\bGTK\b`)},
	// AI & ML
	{"AI", regexp.MustCompile(`\bAI\b`)},
	{"ML", regexp.MustCompile(`\bML\b`)},
	{"LLM", regexp.MustCompile(`\bLLM\b`)},
	{"MCP", regexp.MustCompile(`\bMCP\b`)},
	{"NLP", regexp.MustCompile(`\bNLP\b`)},
	// Concepts
	{"CLI", regexp.MustCompile(`\bCLI\b`)},
	{"API", regexp.MustCompile(`\bAPI\b`)},
	{"WebSocket", regexp.MustCompile(`\bWebSocket\b`)},
	{"WASM", regexp.MustCompile(`\b(?:WASM|WebAssembly)\b`)},
	{"NIO", regexp.MustCompile(`\bNIO\b`)},
	{"XML", regexp.MustCompile(`\bXML\b`)},
	{"JSON", regexp.MustCompile(`\bJSON\b`)},
	{"SQL", regexp.MustCompile(`\bSQL\b`)},
	{"CSS", regexp.MustCompile(`\bCSS\b`)},
	{"HTML", regexp.MustCompile(`\bHTML\b`)},
	{"OAuth", regexp.MustCompile(`\bOAuth\b`)},
	{"SSO", regexp.MustCompile(`\bSSO\b`)},
}

// ExtractSkillsFromText finds known technology keywords in text.
func ExtractSkillsFromText(text string) []string {
	if text == "" {
		return nil
	}
	var skills []string
	for _, sp := range skillPatterns {
		if sp.pattern.MatchString(text) {
			skills = append(skills, sp.name)
		}
	}
	sort.Strings(skills)
	return skills
}

// MergeSkills merges multiple skill slices, deduplicating case-insensitively.
// Keeps the first casing encountered. Returns sorted.
func MergeSkills(slices ...[]string) []string {
	seen := make(map[string]string)
	for _, s := range slices {
		for _, skill := range s {
			lower := strings.ToLower(skill)
			if _, ok := seen[lower]; !ok {
				seen[lower] = skill
			}
		}
	}
	result := make([]string, 0, len(seen))
	for _, v := range seen {
		result = append(result, v)
	}
	sort.Strings(result)
	return result
}
