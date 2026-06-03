package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"gitlab.com/r3j0/incus-compose/client"
)

type FormattersSuite struct {
	suite.Suite
}

type logResource struct {
	name string
}

func (r logResource) Kind() client.Kind { return client.KindInstance }
func (r logResource) Name() string      { return r.name }
func (r logResource) IncusName() string { return r.name }
func (r logResource) Priority() int     { return client.PriorityInstance }
func (r logResource) IsEnsured() bool   { return false }
func (r logResource) Created() bool     { return false }

func (s *FormattersSuite) TestContainerStatusesFormats() {
	status := ProjectStatus{
		Kind:      "container",
		Name:      "web",
		IncusName: "web-1",
		Image:     "docker.io/nginx:alpine",
		Status:    "Running",
		Addresses: []string{"10.0.0.2", "fd42::2"},
	}

	s.Run("table", func() {
		var buf bytes.Buffer
		statuses := NewContainerStatuses(&buf)
		statuses.Add(status)

		s.Require().NoError(statuses.Table())
		s.Contains(buf.String(), "KIND")
		s.Contains(buf.String(), "web-1")
		s.Contains(buf.String(), "10.0.0.2, fd42::2")
	})

	s.Run("json", func() {
		var buf bytes.Buffer
		statuses := NewContainerStatuses(&buf)
		statuses.Add(status)

		s.Require().NoError(statuses.JSON())
		s.Contains(buf.String(), `"name": "web"`)
		s.Contains(buf.String(), `"addresses": [`)
	})

	s.Run("yaml", func() {
		var buf bytes.Buffer
		statuses := NewContainerStatuses(&buf)
		statuses.Add(status)

		s.Require().NoError(statuses.Yaml())
		s.Contains(buf.String(), "name: web")
		s.Contains(buf.String(), "incus_name: web-1")
	})
}

func (s *FormattersSuite) TestLogFormatterNoColor() {
	var buf bytes.Buffer
	formatter := newLogFormatter(&buf, true)

	formatter.registerService("web")
	formatter.registerService("database")
	formatter.write(client.ActionLog, logResource{name: "web"}, []byte("first\npartial"))
	formatter.write(client.ActionLog, logResource{name: "database"}, []byte("ready\n"))
	formatter.flush()

	output := buf.String()
	s.Contains(output, "web      | first\n")
	s.Contains(output, "database | ready\n")
	s.Contains(output, "web      | partial\n")
}

func (s *FormattersSuite) TestLogFormatterColor() {
	var buf bytes.Buffer
	formatter := newLogFormatter(&buf, false)

	formatter.write(client.ActionLog, logResource{name: "web"}, []byte("hello\n"))
	formatter.flush()

	output := buf.String()
	s.True(strings.Contains(output, "\033["))
	s.Contains(output, "web | ")
	s.Contains(output, "hello\n")
}

func TestFormattersSuite(t *testing.T) {
	suite.Run(t, new(FormattersSuite))
}
