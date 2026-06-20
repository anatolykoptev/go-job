package jobserver

import "github.com/modelcontextprotocol/go-sdk/mcp"

// RegisterTools registers all work-related search tools on the given MCP server.
func RegisterTools(server *mcp.Server) {
	// Search
	registerJobSearch(server)
	registerJobMatchScore(server)
	// Research
	registerResearch(server)
	// Resume
	registerResumeAnalyze(server)
	registerCoverLetterGenerate(server)
	registerResumeTailor(server)
	// Tracker
	registerJobTracker(server)
	// ATS direct tools
	registerATS(server)

	// Algora jobs
	registerAlgoraJobIngest(server)
	// Interview & Career Prep
	registerInterviewPrep(server)
	registerProjectShowcase(server)
	registerPitchGenerate(server)
	registerSkillGap(server)
	// Application Workflow
	registerApplicationPrep(server)
	registerOfferCompare(server)
	registerNegotiationPrep(server)
	// Opportunities (unified action-first pipeline)
	registerOpportunitySearch(server)
	registerOpportunityAnalyze(server)
	registerOpportunityClaim(server)
	// LinkedIn
	registerLinkedIn(server)
	registerLinkedInProfileIngest(server)
	// Master Resume
	registerMasterResumeBuild(server)
	registerResumeGenerate(server)
	registerResumeEnrich(server)
	// Resume Profile & Memory
	registerResumeProfile(server)
	registerResumeMemory(server)
	// Oversize store management
	registerOversize(server)
	// Hunt entry listing (triggers lazy enricher on each call)
	registerHuntList(server)
}
