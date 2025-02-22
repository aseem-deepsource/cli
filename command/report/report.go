package report

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/DataDog/zstd"
	"github.com/MakeNowJust/heredoc"
	"github.com/deepsourcelabs/cli/utils"
	"github.com/getsentry/sentry-go"
	"github.com/spf13/cobra"
)

type ReportOptions struct {
	Analyzer                    string
	AnalyzerType                string
	Key                         string
	Value                       string
	ValueFile                   string
	SkipCertificateVerification bool
}

// NewCmdVersion returns the current version of cli being used
func NewCmdReport() *cobra.Command {
	opts := ReportOptions{}

	doc := heredoc.Docf(`
		Report artifacts to DeepSource.

		Use %[1]s to specify the analyzer, for example:
		%[2]s

		Use %[3]s to specify the value of the artifact:
		%[4]s

		You can flag combinations as well:
		%[5]s
		`, utils.Yellow("--analyzer"), utils.Cyan("deepsource report --analyzer python"), utils.Yellow("--value"), utils.Cyan("deepsource report --key value"), utils.Cyan("deepsource report --analyzer go --value-file coverage.out"))

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Report artifacts to DeepSource",
		Long:  doc,
		Args:  utils.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			returnCode := opts.Run()
			sentry.Flush(2 * time.Second)
			defer os.Exit(returnCode)
		},
	}

	// --repo, -r flag
	cmd.Flags().StringVar(&opts.Analyzer, "analyzer", "", "name of the analyzer to report the artifact to (example: test-coverage)")

	cmd.Flags().StringVar(&opts.AnalyzerType, "analyzer-type", "", "type of the analyzer (example: community)")

	cmd.Flags().StringVar(&opts.Key, "key", "", "shortcode of the language (example: go)")

	cmd.Flags().StringVar(&opts.Value, "value", "", "value of the artifact")

	cmd.Flags().StringVar(&opts.ValueFile, "value-file", "", "path to the artifact value file")

	// --skip-verify flag to skip SSL certificate verification while reporting test coverage data.
	cmd.Flags().BoolVar(&opts.SkipCertificateVerification, "skip-verify", false, "skip SSL certificate verification while sending the test coverage data")

	return cmd
}

func (opts *ReportOptions) Run() int {
	// Verify the env variables
	dsn := os.Getenv("DEEPSOURCE_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DeepSource | Error | Environment variable DEEPSOURCE_DSN not set (or) is empty. You can find it under the repository settings page")
		return 1
	}
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetUser(sentry.User{ID: dsn})
	})

	/////////////////////
	// Command: report //
	/////////////////////

	reportCommandAnalyzerShortcode := strings.TrimSpace(opts.Analyzer)
	reportCommandAnalyzerType := strings.TrimSpace(opts.AnalyzerType)
	reportCommandKey := strings.TrimSpace(opts.Key)
	reportCommandValue := opts.Value
	reportCommandValueFile := strings.TrimSpace(opts.ValueFile)

	// Get current path
	currentDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "DeepSource | Error | Unable to identify current directory")
		sentry.CaptureException(err)
		return 1
	}
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetExtra("currentDir", currentDir)
	})

	//////////////////
	// Validate Key //
	//////////////////

	supportedKeys := map[string]bool{
		"python":     true,
		"go":         true,
		"javascript": true,
		"ruby":       true,
		"java":       true,
		"scala":      true,
		"php":        true,
		"csharp":     true,
		"cxx":        true,
		"rust":       true,
		"swift":      true,
		"kotlin":     true,
	}

	allowedKeys := func(m map[string]bool) []string {
		keys := make([]string, 0, len(supportedKeys))
		for k := range m {
			keys = append(keys, k)
		}
		return keys
	}

	if reportCommandAnalyzerShortcode == "test-coverage" && !supportedKeys[reportCommandKey] {
		err = fmt.Errorf("DeepSource | Error | Invalid Key: %s (Supported Keys: %v)", reportCommandKey, allowedKeys(supportedKeys))
		fmt.Fprintln(os.Stderr, err)
		sentry.CaptureException(err)
		return 1
	}

	//////////////////
	// Validate DSN //
	//////////////////

	// Protocol
	dsnSplitProtocolBody := strings.Split(dsn, "://")

	// Validate DSN parsing
	if len(dsnSplitProtocolBody) != 2 {
		err = errors.New("DeepSource | Error | Invalid DSN. Cross verify DEEPSOURCE_DSN value against the settings page of the repository")
		fmt.Fprintln(os.Stderr, err)
		sentry.CaptureException(err)
		return 1
	}

	// Check for valid protocol
	if !strings.HasPrefix(dsnSplitProtocolBody[0], "http") {
		err = errors.New("DeepSource | Error | DSN specified should start with http(s). Cross verify DEEPSOURCE_DSN value against the settings page of the repository")
		fmt.Fprintln(os.Stderr, err)
		sentry.CaptureException(err)
		return 1
	}
	dsnProtocol := dsnSplitProtocolBody[0]

	// Parse body of the DSN
	dsnSplitTokenHost := strings.Split(dsnSplitProtocolBody[1], "@")

	// Validate DSN parsing
	if len(dsnSplitTokenHost) != 2 {
		err = errors.New("DeepSource | Error | Invalid DSN. Cross verify DEEPSOURCE_DSN value against the settings page of the repository")
		fmt.Fprintln(os.Stderr, err)
		sentry.CaptureException(err)
		return 1
	}

	// Set values parsed from DSN
	dsnHost := dsnSplitTokenHost[1]

	///////////////////////
	// Generate metadata //
	///////////////////////

	// Access token
	dsnAccessToken := dsnSplitTokenHost[0]

	// Head Commit OID
	headCommitOID, warning, err := gitGetHead(currentDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DeepSource | Error | Unable to get commit OID HEAD. Make sure you are running the CLI from a git repository")
		log.Println(err)
		sentry.CaptureException(err)
		return 1
	}
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetExtra("headCommitOID", headCommitOID)
	})

	// Flag validation
	if reportCommandValue == "" && reportCommandValueFile == "" {
		fmt.Fprintln(os.Stderr, "DeepSource | Error | '--value' (or) '--value-file' not passed")
		return 1
	}

	var analyzerShortcode string
	var analyzerType string
	var artifactKey string
	var artifactValue string

	analyzerShortcode = reportCommandAnalyzerShortcode
	analyzerType = reportCommandAnalyzerType
	artifactKey = reportCommandKey

	if reportCommandValue != "" {
		artifactValue = reportCommandValue
	}

	if reportCommandValueFile != "" {
		// Check file size
		_, err := os.Stat(reportCommandValueFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "DeepSource | Error | Unable to read specified value file:", reportCommandValueFile)
			sentry.CaptureException(err)
			return 1
		}

		valueBytes, err := os.ReadFile(reportCommandValueFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "DeepSource | Error | Unable to read specified value file:", reportCommandValueFile)
			sentry.CaptureException(err)
			return 1
		}

		artifactValue = string(valueBytes)
	}

	// Query DeepSource API to check if compression is supported
	q := ReportQuery{Query: graphqlCheckCompressed}

	qBytes, err := json.Marshal(q)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DeepSource | Error | Failed to marshal query:", err)
		sentry.CaptureException(err)
		return 1
	}

	r, err := makeQuery(
		dsnProtocol+"://"+dsnHost+"/graphql/cli/",
		qBytes,
		"application/json",
		opts.SkipCertificateVerification,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DeepSource | Error | Failed to make query:", err)
		sentry.CaptureException(err)
		return 1
	}

	// res is a struct to unmarshal the response to check if compression is supported
	var res struct {
		Data struct {
			Type struct {
				InputFields []struct {
					Name string `json:"name"`
				} `json:"inputFields"`
			} `json:"__type"`
		} `json:"data"`
	}

	err = json.Unmarshal(r, &res)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DeepSource | Error | Failed to unmarshal response:", err)
		sentry.CaptureException(err)
		return 1
	}

	reportMeta := make(map[string]interface{})
	reportMeta["workDir"] = currentDir

	// Compress the value if compression is supported
	for _, inputField := range res.Data.Type.InputFields {
		if inputField.Name == "compressed" {
			// Compress the byte array
			var compressedBytes []byte
			compressLevel := 20
			compressedBytes, err = zstd.CompressLevel(compressedBytes, []byte(artifactValue), compressLevel)
			if err != nil {
				fmt.Fprintln(os.Stderr, "DeepSource | Error | Failed to compress value file:", reportCommandValueFile)
				sentry.CaptureException(err)
				return 1
			}

			// Base64 encode the compressed byte array
			artifactValue = base64.StdEncoding.EncodeToString(compressedBytes)

			// Set the compression flag
			reportMeta["compressed"] = "True"
		}
	}

	////////////////////
	// Generate query //
	////////////////////

	queryInput := ReportQueryInput{
		AccessToken:       dsnAccessToken,
		CommitOID:         headCommitOID,
		ReporterName:      "cli",
		ReporterVersion:   CliVersion,
		Key:               artifactKey,
		Data:              artifactValue,
		AnalyzerShortcode: analyzerShortcode,
		// AnalyzerType:      analyzerType,  // Add this in the later steps, only is the analyzer type is passed.
		// This makes sure that the cli is always backwards compatible. The API is designed to accept analyzer type only if it is passed.
		Metadata: reportMeta,
	}

	query := ReportQuery{Query: reportGraphqlQuery}
	// Check if analyzerType is passed and add it to the queryInput
	if analyzerType != "" {
		queryInput.AnalyzerType = analyzerType
	}
	//  Pass queryInput to the query
	query.Variables.Input = queryInput

	// Marshal request body
	queryBodyBytes, err := json.Marshal(query)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DeepSource | Error | Unable to marshal query body")
		sentry.CaptureException(err)
		return 1
	}

	queryResponseBody, err := makeQuery(
		dsnProtocol+"://"+dsnHost+"/graphql/cli/",
		queryBodyBytes,
		"application/json",
		opts.SkipCertificateVerification,
	)
	if err != nil {
		// Make Query without message field.
		query := ReportQuery{Query: reportGraphqlQueryOld}
		query.Variables.Input = queryInput
		queryBodyBytes, err := json.Marshal(query)
		if err != nil {
			fmt.Fprintln(os.Stderr, "DeepSource | Error | Unable to marshal query body")
			sentry.CaptureException(err)
			return 1
		}
		queryResponseBody, err = makeQuery(
			dsnProtocol+"://"+dsnHost+"/graphql/cli/",
			queryBodyBytes,
			"application/json",
			opts.SkipCertificateVerification,
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, "DeepSource | Error | Reporting failed |", err)
			sentry.CaptureException(err)
			return 1
		}
	}
	// Parse query's response body
	queryResponse := QueryResponse{}
	err = json.Unmarshal(queryResponseBody, &queryResponse)
	if err != nil {
		fmt.Fprintln(os.Stderr, "DeepSource | Error | Unable to parse response body")
		sentry.CaptureException(err)
		return 1
	}

	// Check for errors in response body
	// Response format:
	// {
	//   "data": {
	//     "createArtifact": {
	//       "ok": false,
	//       "error": "No repository found attached with the access token: dasdsds"
	//     }
	//   }
	// }

	if !queryResponse.Data.CreateArtifact.Ok {
		fmt.Fprintln(os.Stderr, "DeepSource | Error | Reporting failed |", queryResponse.Data.CreateArtifact.Error)
		sentry.CaptureException(errors.New(queryResponse.Data.CreateArtifact.Error))
		return 1
	}

	fmt.Printf("DeepSource | Artifact published successfully\n\n")
	fmt.Printf("Analyzer  %s\n", analyzerShortcode)
	fmt.Printf("Key       %s\n", artifactKey)
	if queryResponse.Data.CreateArtifact.Message != "" {
		fmt.Printf("Message   %s\n", queryResponse.Data.CreateArtifact.Message)
	}
	if warning != "" {
		fmt.Print(warning)
	}
	return 0
}
