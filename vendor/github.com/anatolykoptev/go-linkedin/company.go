package linkedin

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

func (c *Client) GetCompany(ctx context.Context, slug string) (*Company, error) {
	slug = normalizeCompanySlug(slug)
	endpoint := fmt.Sprintf("/voyager/api/organization/companies?q=universalName&universalName=%s", url.QueryEscape(slug))
	body, err := c.do(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("get company %s: %w", slug, err)
	}
	resp, err := parseVoyagerResponse(body)
	if err != nil {
		return nil, err
	}
	var companyData struct {
		EntityURN    string `json:"entityUrn"`
		Name         string `json:"name"`
		Description  string `json:"description"`
		IndustryName string `json:"industryName"`
		StaffRange   string `json:"staffCountRange"`
		Headquarters *struct {
			City string `json:"city"`
		} `json:"headquarter"`
		FoundedYear int      `json:"foundedOn"`
		Specialties []string `json:"specialities"`
		WebsiteURL  string   `json:"companyPageUrl"`
		StaffCount  int      `json:"staffCount"`
	}
	if err := safeUnmarshal(resp.Data, &companyData); err != nil {
		return nil, err
	}
	hq := ""
	if companyData.Headquarters != nil {
		hq = companyData.Headquarters.City
	}
	return &Company{
		URN:           companyData.EntityURN,
		Name:          companyData.Name,
		Description:   companyData.Description,
		Industry:      companyData.IndustryName,
		Size:          companyData.StaffRange,
		Headquarters:  hq,
		Founded:       companyData.FoundedYear,
		Specialties:   companyData.Specialties,
		Website:       companyData.WebsiteURL,
		EmployeeCount: companyData.StaffCount,
	}, nil
}

func normalizeCompanySlug(slug string) string {
	slug = strings.TrimSpace(slug)
	if idx := strings.Index(slug, "linkedin.com/company/"); idx >= 0 {
		slug = slug[idx+len("linkedin.com/company/"):]
	}
	return strings.TrimRight(slug, "/")
}
