package main

import (
	"testing"

	gokoalaEngine "github.com/PDOK/gokoala/internal/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

// runAppWithCapturedOrigins runs an app using the production cliFlags while capturing the
// parsed CORS allowed origins instead of starting the actual server.
func runAppWithCapturedOrigins(t *testing.T, args ...string) []string {
	t.Helper()
	var captured []string
	app := &cli.App{
		Name:  "gokoala-test",
		Flags: cliFlags,
		Action: func(c *cli.Context) error {
			captured = c.StringSlice(corsAllowedOriginsFlag)
			return nil
		},
	}
	require.NoError(t, app.Run(append([]string{"gokoala"}, args...)))
	return captured
}

func TestCORSAllowedOriginsFlag_FromCLI(t *testing.T) {
	// both repeated flags and comma-separated values are supported
	origins := runAppWithCapturedOrigins(t,
		"--config-file", "examples/config_all.yaml",
		"--cors-allowed-origins", "https://example.com,http://localhost:8080",
		"--cors-allowed-origins", "https://app.example.com",
	)
	assert.Equal(t, []string{
		"https://example.com",
		"http://localhost:8080",
		"https://app.example.com",
	}, origins)
}

func TestCORSAllowedOriginsFlag_FromEnv(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://example.com, https://app.example.com")

	origins := runAppWithCapturedOrigins(t, "--config-file", "examples/config_all.yaml")

	// whitespace around entries is trimmed by the CLI parser
	assert.Equal(t, []string{"https://example.com", "https://app.example.com"}, origins)
}

func TestCORSAllowedOriginsFlag_EnvInvalidRejectedAtStartup(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://example.com/path,https://example.com")

	origins := runAppWithCapturedOrigins(t, "--config-file", "examples/config_all.yaml")

	_, err := gokoalaEngine.NewEngine("internal/engine/testdata/config_minimal.yaml",
		"internal/engine/testdata/test_theme.yaml", "", false, false, origins)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid CORS allowed origin")
}

func TestCORSAllowedOriginsFlag_EnvValidAcceptedAtStartup(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://example.com,https://app.example.com")

	origins := runAppWithCapturedOrigins(t, "--config-file", "examples/config_all.yaml")

	engine, err := gokoalaEngine.NewEngine("internal/engine/testdata/config_minimal.yaml",
		"internal/engine/testdata/test_theme.yaml", "", false, false, origins)
	require.NoError(t, err)
	require.NotNil(t, engine)
}
