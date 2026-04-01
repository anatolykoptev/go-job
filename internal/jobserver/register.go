package jobserver

import "github.com/modelcontextprotocol/go-sdk/mcp"

// RegisterTools registers all work-related search tools on the given MCP server.
func RegisterTools(server *mcp.Server) {
	// Search
	registerJobSearch(server)
	registerRemoteWorkSearch(server)
	registerFreelanceSearch(server)
	registerJobMatchScore(server)
	// Research
	registerSalaryResearch(server)
	registerCompanyResearch(server)
	// Resume
	registerResumeAnalyze(server)
	registerCoverLetterGenerate(server)
	registerResumeTailor(server)
	// Tracker
	registerJobTrackerAdd(server)
	registerJobTrackerList(server)
	registerJobTrackerUpdate(server)
	// Person research
	registerPersonResearch(server)
	// Interview & Career Prep
	registerInterviewPrep(server)
	registerProjectShowcase(server)
	registerPitchGenerate(server)
	registerSkillGap(server)
	// Application Workflow
	registerApplicationPrep(server)
	registerOfferCompare(server)
	registerNegotiationPrep(server)
	// Bounties
	registerBountySearch(server)
	registerBountyAttempt(server)
	registerBountyAnalyze(server)
	// Opportunities (unified action-first pipeline)
	registerOpportunitySearch(server)
	registerOpportunityAnalyze(server)
	registerOpportunityClaim(server)
	// Security Bug Bounties
	registerSecurityBountySearch(server)
	// Twitter
	registerTwitterJobSearch(server)
	// LinkedIn
	registerLinkedInProfile(server)
	registerLinkedInCompany(server)
	registerLinkedInJobs(server)
	registerLinkedInSearch(server)
	registerLinkedInPosts(server)
	registerLinkedInRating(server)
	// Master Resume
	registerMasterResumeBuild(server)
	registerResumeGenerate(server)
	registerResumeEnrich(server)
	// Resume Profile & Memory
	registerResumeProfile(server)
	registerResumeMemorySearch(server)
	registerResumeMemoryAdd(server)
	registerResumeMemoryUpdate(server)
}
