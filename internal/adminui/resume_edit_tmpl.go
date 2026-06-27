package adminui

// resumeEditTmplSrc is the HTML content fragment for the resume editor page.
// All user-supplied data fields are rendered via {{.Field}} — html/template
// auto-escapes every value, preventing XSS from DB content.
// The CSRF token is safe to embed verbatim (it is hex-encoded MAC output).
const resumeEditTmplSrc = `<style>
.re{max-width:860px;margin:0 auto;padding:2rem 1.5rem;font-family:system-ui,sans-serif;color:#e2e8f0}
.re-nav{margin-bottom:1.5rem;display:flex;gap:1rem;align-items:center}
.re-nav a{color:#60a5fa;text-decoration:none;font-size:.9rem}
.re-nav a:hover{color:#93c5fd}
.re-section{background:#1e293b;border-radius:.5rem;padding:1.25rem;margin-bottom:1.5rem}
.re-section h3{margin:0 0 1rem;font-size:.85rem;font-weight:600;text-transform:uppercase;letter-spacing:.08em;color:#64748b}
.re-form-grid{display:grid;grid-template-columns:1fr 1fr;gap:.75rem}
.re-form-full{grid-column:1/-1}
.re-label{display:block;font-size:.8rem;color:#94a3b8;margin-bottom:.25rem}
.re-input,.re-select,.re-textarea{width:100%;padding:.45rem .7rem;background:#0f172a;border:1px solid #334155;border-radius:.375rem;color:#e2e8f0;font-size:.875rem;box-sizing:border-box}
.re-textarea{min-height:4rem;resize:vertical}
.re-btn{padding:.45rem 1rem;background:#2563eb;color:#fff;border:none;border-radius:.375rem;cursor:pointer;font-size:.85rem}
.re-btn:hover{background:#1d4ed8}
.re-btn-sm{padding:.3rem .7rem;font-size:.8rem}
.re-btn-del{background:#7f1d1d;color:#fca5a5}
.re-btn-del:hover{background:#991b1b}
.re-row{display:flex;align-items:center;gap:.75rem;padding:.5rem 0;border-bottom:1px solid #1e293b}
.re-row:last-child{border-bottom:none}
.re-row .name{flex:1;font-size:.875rem;color:#e2e8f0}
.re-row .meta{font-size:.78rem;color:#94a3b8}
.re-chip{display:inline-block;background:#0f172a;border-radius:.25rem;padding:.15rem .5rem;font-size:.78rem;color:#94a3b8}
.re-add-form{margin-top:1rem;padding-top:1rem;border-top:1px solid #334155}
.re-add-form h4{margin:0 0 .75rem;font-size:.8rem;color:#64748b;font-weight:600}
</style>
<div class="re">
  <div class="re-nav">
    <a href="/admin/resume">&larr; View resume</a>
    <span style="color:#334155">|</span>
    <span style="color:#94a3b8;font-size:.85rem">Edit Resume</span>
  </div>

  {{/* ─── Person header ─── */}}
  <div class="re-section">
    <h3>Person</h3>
    <form method="POST" action="/admin/resume/person">
      <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
      <div class="re-form-grid">
        <div>
          <label class="re-label">Name</label>
          <input class="re-input" type="text" name="name" value="{{.Person.Name}}">
        </div>
        <div>
          <label class="re-label">Email</label>
          <input class="re-input" type="text" name="email" value="{{.Person.Email}}">
        </div>
        <div>
          <label class="re-label">Phone</label>
          <input class="re-input" type="text" name="phone" value="{{.Person.Phone}}">
        </div>
        <div>
          <label class="re-label">Location</label>
          <input class="re-input" type="text" name="location" value="{{.Person.Location}}">
        </div>
        <div class="re-form-full">
          <label class="re-label">Summary</label>
          <textarea class="re-textarea" name="summary">{{.Person.Summary}}</textarea>
        </div>
        <div>
          <label class="re-label">Upwork Headline (max 70 chars)</label>
          <input class="re-input" type="text" name="headline" maxlength="70" value="{{.Person.Headline}}">
        </div>
        <div>
          <label class="re-label">Upwork Hourly Rate ($/hr)</label>
          <input class="re-input" type="number" name="hourly_rate" step="0.01" min="0" value="{{.HourlyRateStr}}">
        </div>
      </div>
      <div style="margin-top:.75rem">
        <button class="re-btn" type="submit">Save person</button>
      </div>
    </form>
  </div>

  {{/* ─── Experiences ─── */}}
  <div class="re-section">
    <h3>Experiences ({{len .Experiences}})</h3>
    {{range .Experiences}}
    <div class="re-row">
      <div class="name">{{.Title}} <span class="meta">@ {{.Company}}</span></div>
      <div class="meta">{{.StartDate}}{{if .EndDate}} – {{.EndDate}}{{end}}</div>
      <form method="POST" action="/admin/resume/experience/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button class="re-btn re-btn-sm re-btn-del" type="submit"
          onclick="return confirm('Delete experience?')">Del</button>
      </form>
    </div>
    {{end}}
    <div class="re-add-form">
      <h4>Add experience</h4>
      <form method="POST" action="/admin/resume/experience">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <div class="re-form-grid">
          <div>
            <label class="re-label">Title *</label>
            <input class="re-input" type="text" name="title" required>
          </div>
          <div>
            <label class="re-label">Company *</label>
            <input class="re-input" type="text" name="company" required>
          </div>
          <div>
            <label class="re-label">Location</label>
            <input class="re-input" type="text" name="location">
          </div>
          <div>
            <label class="re-label">Start date (e.g. 2022-01)</label>
            <input class="re-input" type="text" name="start_date" placeholder="2022-01">
          </div>
          <div>
            <label class="re-label">End date (blank = present)</label>
            <input class="re-input" type="text" name="end_date" placeholder="Present">
          </div>
          <div>
            <label class="re-label">Description</label>
            <input class="re-input" type="text" name="description">
          </div>
        </div>
        <div style="margin-top:.75rem">
          <button class="re-btn" type="submit">Add experience</button>
        </div>
      </form>
    </div>
  </div>

  {{/* ─── Skills ─── */}}
  <div class="re-section">
    <h3>Skills ({{len .Skills}})</h3>
    {{range .Skills}}
    <div class="re-row">
      <div class="name">{{.Name}} <span class="re-chip">{{.Category}}</span></div>
      <form method="POST" action="/admin/resume/skill/{{.ID}}/level" style="display:inline;display:flex;gap:.5rem;align-items:center">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <select class="re-select" name="level" style="width:auto;padding:.25rem .5rem;font-size:.8rem">
          <option value="beginner"{{if eq .Level "beginner"}} selected{{end}}>beginner</option>
          <option value="intermediate"{{if eq .Level "intermediate"}} selected{{end}}>intermediate</option>
          <option value="advanced"{{if eq .Level "advanced"}} selected{{end}}>advanced</option>
          <option value="expert"{{if eq .Level "expert"}} selected{{end}}>expert</option>
        </select>
        <button class="re-btn re-btn-sm" type="submit">Set</button>
      </form>
      <form method="POST" action="/admin/resume/skill/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button class="re-btn re-btn-sm re-btn-del" type="submit"
          onclick="return confirm('Delete skill?')">Del</button>
      </form>
    </div>
    {{end}}
    <div class="re-add-form">
      <h4>Add skill</h4>
      <form method="POST" action="/admin/resume/skill">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <div class="re-form-grid">
          <div>
            <label class="re-label">Name *</label>
            <input class="re-input" type="text" name="name" required>
          </div>
          <div>
            <label class="re-label">Category</label>
            <input class="re-input" type="text" name="category" placeholder="languages">
          </div>
          <div>
            <label class="re-label">Level</label>
            <select class="re-select" name="level">
              <option value="beginner">beginner</option>
              <option value="intermediate" selected>intermediate</option>
              <option value="advanced">advanced</option>
              <option value="expert">expert</option>
            </select>
          </div>
        </div>
        <div style="margin-top:.75rem">
          <button class="re-btn" type="submit">Add skill</button>
        </div>
      </form>
    </div>
  </div>

  {{/* ─── Achievements ─── */}}
  <div class="re-section">
    <h3>Achievements ({{len .Achievements}})</h3>
    {{range .Achievements}}
    <div class="re-row">
      <div class="name">{{.Text}}
        {{if .Metric}}<span class="re-chip">{{.Metric}}{{if .Value}}: {{.Value}}{{end}}</span>{{end}}
      </div>
      <form method="POST" action="/admin/resume/achievement/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button class="re-btn re-btn-sm re-btn-del" type="submit"
          onclick="return confirm('Delete achievement?')">Del</button>
      </form>
    </div>
    {{end}}
    <div class="re-add-form">
      <h4>Add achievement</h4>
      <form method="POST" action="/admin/resume/achievement">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <div class="re-form-grid">
          <div class="re-form-full">
            <label class="re-label">Text *</label>
            <input class="re-input" type="text" name="text" required>
          </div>
          <div>
            <label class="re-label">Metric</label>
            <input class="re-input" type="text" name="metric" placeholder="latency">
          </div>
          <div>
            <label class="re-label">Value</label>
            <input class="re-input" type="text" name="value" placeholder="10x">
          </div>
          <div class="re-form-full">
            <label class="re-label">Context</label>
            <input class="re-input" type="text" name="context">
          </div>
        </div>
        <div style="margin-top:.75rem">
          <button class="re-btn" type="submit">Add achievement</button>
        </div>
      </form>
    </div>
  </div>

  {{/* ─── Domains ─── */}}
  <div class="re-section">
    <h3>Domains ({{len .Domains}})</h3>
    {{range .Domains}}
    <div class="re-row">
      <div class="name">{{.Name}}</div>
      <form method="POST" action="/admin/resume/domain/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button class="re-btn re-btn-sm re-btn-del" type="submit"
          onclick="return confirm('Delete domain?')">Del</button>
      </form>
    </div>
    {{end}}
    <div class="re-add-form">
      <h4>Add domain</h4>
      <form method="POST" action="/admin/resume/domain">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <div>
          <label class="re-label">Name *</label>
          <input class="re-input" type="text" name="name" required>
        </div>
        <div style="margin-top:.75rem">
          <button class="re-btn" type="submit">Add domain</button>
        </div>
      </form>
    </div>
  </div>

  {{/* ─── Methodologies ─── */}}
  <div class="re-section">
    <h3>Methodologies ({{len .Methodologies}})</h3>
    {{range .Methodologies}}
    <div class="re-row">
      <div class="name">{{.Name}}{{if .Description}} <span class="meta">— {{.Description}}</span>{{end}}</div>
      <form method="POST" action="/admin/resume/methodology/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button class="re-btn re-btn-sm re-btn-del" type="submit"
          onclick="return confirm('Delete methodology?')">Del</button>
      </form>
    </div>
    {{end}}
    <div class="re-add-form">
      <h4>Add methodology</h4>
      <form method="POST" action="/admin/resume/methodology">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <div class="re-form-grid">
          <div>
            <label class="re-label">Name *</label>
            <input class="re-input" type="text" name="name" required>
          </div>
          <div>
            <label class="re-label">Description</label>
            <input class="re-input" type="text" name="description">
          </div>
        </div>
        <div style="margin-top:.75rem">
          <button class="re-btn" type="submit">Add methodology</button>
        </div>
      </form>
    </div>
  </div>
  {{/* ─── Projects ─── */}}
  <div class="re-section">
    <h3>Projects ({{len .Projects}})</h3>
    {{range .Projects}}
    <div class="re-row">
      <div class="name">{{.Name}}{{if .Description}} <span class="meta">— {{.Description}}</span>{{end}}
        {{if .URL}}<a href="{{.URL}}" style="font-size:.78rem;color:#60a5fa" target="_blank">link</a>{{end}}
      </div>
      <form method="POST" action="/admin/resume/project/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button class="re-btn re-btn-sm re-btn-del" type="submit"
          onclick="return confirm(&#39;Delete project?&#39;)">Del</button>
      </form>
    </div>
    {{end}}
    <div class="re-add-form">
      <h4>Add project</h4>
      <form method="POST" action="/admin/resume/project">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <div class="re-form-grid">
          <div>
            <label class="re-label">Name *</label>
            <input class="re-input" type="text" name="name" required>
          </div>
          <div>
            <label class="re-label">URL</label>
            <input class="re-input" type="text" name="url" placeholder="https://...">
          </div>
          <div class="re-form-full">
            <label class="re-label">Description</label>
            <input class="re-input" type="text" name="description">
          </div>
        </div>
        <div style="margin-top:.75rem">
          <button class="re-btn" type="submit">Add project</button>
        </div>
      </form>
    </div>
  </div>

  {{/* ─── Educations ─── */}}
  <div class="re-section">
    <h3>Educations ({{len .Educations}})</h3>
    {{range .Educations}}
    <div class="re-row">
      <div class="name">{{.School}}{{if .Degree}} <span class="meta">— {{.Degree}}{{if .Field}} / {{.Field}}{{end}}</span>{{end}}
        {{if .StartDate}}<span class="meta"> {{.StartDate}}–{{.EndDate}}</span>{{end}}
      </div>
      <form method="POST" action="/admin/resume/education/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button class="re-btn re-btn-sm re-btn-del" type="submit"
          onclick="return confirm(&#39;Delete education?&#39;)">Del</button>
      </form>
    </div>
    {{end}}
    <div class="re-add-form">
      <h4>Add education</h4>
      <form method="POST" action="/admin/resume/education">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <div class="re-form-grid">
          <div>
            <label class="re-label">School *</label>
            <input class="re-input" type="text" name="school" required>
          </div>
          <div>
            <label class="re-label">Degree</label>
            <input class="re-input" type="text" name="degree">
          </div>
          <div>
            <label class="re-label">Field</label>
            <input class="re-input" type="text" name="field">
          </div>
          <div>
            <label class="re-label">GPA</label>
            <input class="re-input" type="text" name="gpa">
          </div>
          <div>
            <label class="re-label">Start date</label>
            <input class="re-input" type="text" name="start_date" placeholder="2018-09">
          </div>
          <div>
            <label class="re-label">End date</label>
            <input class="re-input" type="text" name="end_date" placeholder="2022-05">
          </div>
        </div>
        <div style="margin-top:.75rem">
          <button class="re-btn" type="submit">Add education</button>
        </div>
      </form>
    </div>
  </div>

  {{/* ─── Certifications ─── */}}
  <div class="re-section">
    <h3>Certifications ({{len .Certifications}})</h3>
    {{range .Certifications}}
    <div class="re-row">
      <div class="name">{{.Name}}{{if .Issuer}} <span class="meta">— {{.Issuer}}</span>{{end}}
        {{if .Year}}<span class="meta"> ({{.Year}})</span>{{end}}
        {{if .URL}}<a href="{{.URL}}" style="font-size:.78rem;color:#60a5fa" target="_blank">link</a>{{end}}
      </div>
      <form method="POST" action="/admin/resume/certification/{{.ID}}/delete" style="display:inline">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button class="re-btn re-btn-sm re-btn-del" type="submit"
          onclick="return confirm(&#39;Delete certification?&#39;)">Del</button>
      </form>
    </div>
    {{end}}
    <div class="re-add-form">
      <h4>Add certification</h4>
      <form method="POST" action="/admin/resume/certification">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <div class="re-form-grid">
          <div>
            <label class="re-label">Name *</label>
            <input class="re-input" type="text" name="name" required>
          </div>
          <div>
            <label class="re-label">Issuer</label>
            <input class="re-input" type="text" name="issuer">
          </div>
          <div>
            <label class="re-label">Year</label>
            <input class="re-input" type="text" name="year" placeholder="2024">
          </div>
          <div class="re-form-full">
            <label class="re-label">URL</label>
            <input class="re-input" type="text" name="url" placeholder="https://...">
          </div>
        </div>
        <div style="margin-top:.75rem">
          <button class="re-btn" type="submit">Add certification</button>
        </div>
      </form>
    </div>
  </div>

</div>`
