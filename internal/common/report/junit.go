package report

import "encoding/xml"

// Export JUnit XML : un testsuite par rapport, un testcase par check,
// classname = nom de l'item. Compatible GitLab CI / Jenkins / Forgejo Actions.

type junitMsg struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

type junitCase struct {
	Name      string    `xml:"name,attr"`
	Classname string    `xml:"classname,attr"`
	Failure   *junitMsg `xml:"failure,omitempty"`
	Skipped   *junitMsg `xml:"skipped,omitempty"`
	SystemOut string    `xml:"system-out,omitempty"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitTestsuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

func junitXML(r *Report) ([]byte, error) {
	suite := junitSuite{Name: "ocptk-" + r.Kind + "-" + r.Timestamp}
	for _, it := range r.Items {
		for _, c := range it.Checks {
			tc := junitCase{Name: c.Name, Classname: it.Name}
			switch c.Status {
			case Fail:
				tc.Failure = &junitMsg{Message: c.Name, Body: c.Detail}
				suite.Failures++
			case Skip:
				tc.Skipped = &junitMsg{Message: c.Detail}
				suite.Skipped++
			case Warn:
				tc.SystemOut = "WARN: " + c.Detail
			default:
				tc.SystemOut = c.Detail
			}
			suite.Tests++
			suite.Cases = append(suite.Cases, tc)
		}
	}
	out, err := xml.MarshalIndent(junitTestsuites{Suites: []junitSuite{suite}}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}
