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
	endpoint := fmt.Sprintf("%s?q=memberIdentity&memberIdentity=%s&decorationId=com.linkedin.voyager.dash.deco.identity.profile.TopCardSupplementary-138",
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
	// Profile data lives in included[] under $type=com.linkedin.voyager.dash.identity.profile.Profile
	profileItems := includedByType(resp.Included, "com.linkedin.voyager.dash.identity.profile.Profile")
	if len(profileItems) > 0 {
		var profileData struct {
			EntityURN        string `json:"entityUrn"`
			FirstName        string `json:"firstName"`
			LastName         string `json:"lastName"`
			Headline         string `json:"headline"`
			Summary          string `json:"summary"`
			IndustryName     string `json:"industryName"`
			PublicIdentifier string `json:"publicIdentifier"`
		}
		if err := safeUnmarshal(profileItems[0], &profileData); err == nil {
			profile.URN = profileData.EntityURN
			profile.FirstName = profileData.FirstName
			profile.LastName = profileData.LastName
			profile.Headline = profileData.Headline
			profile.About = profileData.Summary
			profile.Industry = profileData.IndustryName
		}
	}
	// Location from Geo objects in included
	geoItems := includedByType(resp.Included, "com.linkedin.voyager.dash.common.Geo")
	for _, raw := range geoItems {
		var geo struct {
			Name       string `json:"defaultLocalizedName"`
			CountryURN string `json:"countryUrn"`
		}
		if safeUnmarshal(raw, &geo) == nil && geo.CountryURN == "" && geo.Name != "" {
			profile.Location = geo.Name
			break
		}
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
