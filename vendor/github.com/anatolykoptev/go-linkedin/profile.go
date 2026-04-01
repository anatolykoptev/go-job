package linkedin

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

const profileEndpoint = "/voyager/api/identity/dash/profiles"

// GetProfile fetches a full LinkedIn profile by handle (vanity name).
// Makes 3 API calls: profile, skills, contact info.
func (c *Client) GetProfile(ctx context.Context, handle string) (*Profile, error) {
	handle = normalizeHandle(handle)
	profile, profileID, err := c.getBasicProfile(ctx, handle)
	if err != nil {
		return nil, err
	}
	if skills, err := c.GetSkills(ctx, profileID); err == nil {
		profile.Skills = skills
	}
	if contact, err := c.GetContactInfo(ctx, profileID); err == nil {
		profile.ContactInfo = contact
	}
	return profile, nil
}

func (c *Client) getBasicProfile(ctx context.Context, handle string) (*Profile, string, error) {
	endpoint := fmt.Sprintf("%s?q=memberIdentity&memberIdentity=%s&decorationId=com.linkedin.voyager.dash.deco.identity.profile.WebTopCardCore-20",
		profileEndpoint, url.QueryEscape(handle))
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, "", fmt.Errorf("get profile %s: %w", handle, err)
	}
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, "", err
	}
	profile := &Profile{
		ProfileURL: fmt.Sprintf("https://www.linkedin.com/in/%s", handle),
	}
	var profileData struct {
		EntityURN       string `json:"entityUrn"`
		FirstName       string `json:"firstName"`
		LastName        string `json:"lastName"`
		Headline        string `json:"headline"`
		LocationName    string `json:"locationName"`
		Summary         string `json:"summary"`
		IndustryName    string `json:"industryName"`
		ConnectionCount int    `json:"numConnections"`
		FollowerCount   int    `json:"followingCount"`
	}
	if err := safeUnmarshal(resp.Data, &profileData); err == nil {
		profile.URN = profileData.EntityURN
		profile.FirstName = profileData.FirstName
		profile.LastName = profileData.LastName
		profile.Headline = profileData.Headline
		profile.Location = profileData.LocationName
		profile.About = profileData.Summary
		profile.Industry = profileData.IndustryName
		profile.ConnectionCount = profileData.ConnectionCount
		profile.FollowerCount = profileData.FollowerCount
	}
	profile.Experiences = parseExperiences(resp.Included)
	profile.Educations = parseEducations(resp.Included)
	profile.Certifications = parseCertifications(resp.Included)
	profileID := ExtractProfileID(profile.URN)
	return profile, profileID, nil
}

func normalizeHandle(handle string) string {
	handle = strings.TrimSpace(handle)
	if idx := strings.Index(handle, "linkedin.com/in/"); idx >= 0 {
		handle = handle[idx+len("linkedin.com/in/"):]
	}
	return strings.TrimRight(handle, "/")
}

// ExtractProfileID extracts the ID from a URN like "urn:li:fsd_profile:ACoAAB..."
func ExtractProfileID(urn string) string {
	parts := strings.Split(urn, ":")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return urn
}
