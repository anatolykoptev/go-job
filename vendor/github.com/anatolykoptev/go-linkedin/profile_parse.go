package linkedin

import "encoding/json"

func parseExperiences(included []json.RawMessage) []Experience {
	items := includedByType(included, "com.linkedin.voyager.dash.identity.profile.Position")
	var exps []Experience
	for _, raw := range items {
		var pos struct {
			Title        string `json:"title"`
			CompanyName  string `json:"companyName"`
			CompanyURN   string `json:"companyUrn"`
			LocationName string `json:"locationName"`
			Description  string `json:"description"`
			TimePeriod   *struct {
				StartDate *struct {
					Year  int `json:"year"`
					Month int `json:"month"`
				} `json:"startDate"`
				EndDate *struct {
					Year  int `json:"year"`
					Month int `json:"month"`
				} `json:"endDate"`
			} `json:"timePeriod"`
		}
		if json.Unmarshal(raw, &pos) != nil {
			continue
		}
		exp := Experience{
			Title:       pos.Title,
			Company:     pos.CompanyName,
			CompanyURN:  pos.CompanyURN,
			Location:    pos.LocationName,
			Description: pos.Description,
		}
		if pos.TimePeriod != nil {
			if pos.TimePeriod.StartDate != nil {
				exp.StartDate = YearMonth{Year: pos.TimePeriod.StartDate.Year, Month: pos.TimePeriod.StartDate.Month}
			}
			if pos.TimePeriod.EndDate != nil {
				exp.EndDate = &YearMonth{Year: pos.TimePeriod.EndDate.Year, Month: pos.TimePeriod.EndDate.Month}
			}
		}
		exps = append(exps, exp)
	}
	return exps
}

func parseEducations(included []json.RawMessage) []Education {
	items := includedByType(included, "com.linkedin.voyager.dash.identity.profile.Education")
	var edus []Education
	for _, raw := range items {
		var edu struct {
			SchoolName   string `json:"schoolName"`
			DegreeName   string `json:"degreeName"`
			FieldOfStudy string `json:"fieldOfStudy"`
			Description  string `json:"description"`
			TimePeriod   *struct {
				StartDate *struct {
					Year int `json:"year"`
				} `json:"startDate"`
				EndDate *struct {
					Year int `json:"year"`
				} `json:"endDate"`
			} `json:"timePeriod"`
		}
		if json.Unmarshal(raw, &edu) != nil {
			continue
		}
		e := Education{
			School:      edu.SchoolName,
			Degree:      edu.DegreeName,
			Field:       edu.FieldOfStudy,
			Description: edu.Description,
		}
		if edu.TimePeriod != nil {
			if edu.TimePeriod.StartDate != nil {
				e.StartYear = edu.TimePeriod.StartDate.Year
			}
			if edu.TimePeriod.EndDate != nil {
				e.EndYear = edu.TimePeriod.EndDate.Year
			}
		}
		edus = append(edus, e)
	}
	return edus
}

func parseCertifications(included []json.RawMessage) []Certification {
	items := includedByType(included, "com.linkedin.voyager.dash.identity.profile.Certification")
	var certs []Certification
	for _, raw := range items {
		var cert struct {
			Name          string `json:"name"`
			Authority     string `json:"authority"`
			LicenseNumber string `json:"licenseNumber"`
		}
		if json.Unmarshal(raw, &cert) != nil {
			continue
		}
		certs = append(certs, Certification{
			Name:          cert.Name,
			Authority:     cert.Authority,
			LicenseNumber: cert.LicenseNumber,
		})
	}
	return certs
}
