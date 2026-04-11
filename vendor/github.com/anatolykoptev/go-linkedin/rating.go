package linkedin

// ComputeRating calculates profile quality metrics from profile data and posts.
func ComputeRating(profile *Profile, posts []Post) *ProfileRating {
	r := &ProfileRating{
		ConnectionCount: profile.ConnectionCount,
		FollowerCount:   profile.FollowerCount,
	}
	if len(posts) > 0 {
		var totalEngagement int
		for _, p := range posts {
			totalEngagement += p.Likes + p.Comments
		}
		r.AvgEngagement = float64(totalEngagement) / float64(len(posts))
		if len(posts) >= 2 {
			first := posts[len(posts)-1].PublishedAt
			last := posts[0].PublishedAt
			weeks := last.Sub(first).Hours() / (24 * 7)
			if weeks > 0 {
				r.PostFrequency = float64(len(posts)) / weeks
			}
		}
	}
	r.TopEndorsedSkills = topSkills(profile.Skills, 5)
	r.ProfileCompleteness = completeness(profile)
	r.InfluenceScore = influenceScore(r)
	return r
}

func topSkills(skills []Skill, n int) []Skill {
	if len(skills) <= n {
		return skills
	}
	sorted := make([]Skill, len(skills))
	copy(sorted, skills)
	for i := range n {
		maxIdx := i
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].EndorsementCount > sorted[maxIdx].EndorsementCount {
				maxIdx = j
			}
		}
		sorted[i], sorted[maxIdx] = sorted[maxIdx], sorted[i]
	}
	return sorted[:n]
}

func completeness(p *Profile) int {
	score := 0
	if p.FirstName != "" {
		score += 10
	}
	if p.Headline != "" {
		score += 15
	}
	if p.About != "" {
		score += 15
	}
	if p.Location != "" {
		score += 5
	}
	if len(p.Experiences) > 0 {
		score += 20
	}
	if len(p.Educations) > 0 {
		score += 10
	}
	if len(p.Skills) > 0 {
		score += 10
	}
	if p.ContactInfo != nil {
		score += 10
	}
	if p.Industry != "" {
		score += 5
	}
	if score > 100 {
		score = 100
	}
	return score
}

func influenceScore(r *ProfileRating) float64 {
	connScore := min(float64(r.ConnectionCount)/500, float64(1)) * 20
	followerScore := min(float64(r.FollowerCount)/1000, float64(1)) * 25
	engagementScore := min(r.AvgEngagement/50, float64(1)) * 25
	completenessScore := float64(r.ProfileCompleteness) / 100 * 15
	postScore := min(r.PostFrequency/2, float64(1)) * 15
	return connScore + followerScore + engagementScore + completenessScore + postScore
}
