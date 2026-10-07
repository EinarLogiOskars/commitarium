package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

const requestTimeout = 20 * time.Second

type artifactResponse struct {
	Revision int             `json:"revision"`
	Document json.RawMessage `json:"document"`
}

type transitionRequest struct {
	PlanVersion int                        `json:"plan_version"`
	Status      featureartifact.StepStatus `json:"status"`
	CommitID    string                     `json:"commit_id"`
}

type acceptanceTransitionRequest struct {
	PlanVersion            int                                  `json:"plan_version"`
	Status                 featureartifact.AcceptanceTestStatus `json:"status"`
	Note                   string                               `json:"note"`
	ImplementationCommitID string                               `json:"implementation_commit_id"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "commitarium-artifact:", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	if len(arguments) < 2 || (arguments[0] != "plan" && arguments[0] != "acceptance") {
		return usage()
	}
	base, projectID, featureID, err := environment(os.Getenv)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: requestTimeout}
	if arguments[0] == "acceptance" {
		return runAcceptance(client, base, projectID, featureID, arguments[1:], output)
	}
	switch arguments[1] {
	case "show":
		if len(arguments) != 2 {
			return usage()
		}
		artifact, err := getPlan(client, base, projectID, featureID)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, string(artifact.Document))
		return err
	case "start":
		if len(arguments) != 3 {
			return usage()
		}
		return transition(client, base, projectID, featureID, arguments[2], featureartifact.StepInProgress, "", output)
	case "complete":
		if len(arguments) != 4 {
			return usage()
		}
		return transition(client, base, projectID, featureID, arguments[2], featureartifact.StepCompleted, arguments[3], output)
	default:
		return usage()
	}
}

func runAcceptance(
	client *http.Client,
	base, projectID, featureID string,
	arguments []string,
	output io.Writer,
) error {
	if len(arguments) == 1 && arguments[0] == "show" {
		artifact, err := getArtifact(client, base, projectID, featureID, featureartifact.KindAcceptanceTests)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, string(artifact.Document))
		return err
	}
	if len(arguments) < 2 {
		return usage()
	}
	status := featureartifact.AcceptanceTestStatus("")
	note := ""
	switch arguments[0] {
	case "start":
		status = featureartifact.AcceptanceTestRunning
	case "pass":
		status = featureartifact.AcceptanceTestPassed
	case "fail":
		if len(arguments) != 3 {
			return usage()
		}
		status, note = featureartifact.AcceptanceTestFailed, arguments[2]
	case "not-applicable":
		if len(arguments) != 3 {
			return usage()
		}
		status, note = featureartifact.AcceptanceTestNotApplicable, arguments[2]
	default:
		return usage()
	}
	if (status == featureartifact.AcceptanceTestRunning || status == featureartifact.AcceptanceTestPassed) && len(arguments) != 2 {
		return usage()
	}
	return transitionAcceptance(client, base, projectID, featureID, arguments[1], status, note, output)
}

func transitionAcceptance(
	client *http.Client,
	base, projectID, featureID, testID string,
	status featureartifact.AcceptanceTestStatus,
	note string,
	output io.Writer,
) error {
	artifact, err := getArtifact(client, base, projectID, featureID, featureartifact.KindAcceptanceTests)
	if err != nil {
		return err
	}
	tests := featureartifact.AcceptanceTests{}
	if err := json.Unmarshal(artifact.Document, &tests); err != nil {
		return fmt.Errorf("decode acceptance tests: %w", err)
	}
	body, err := json.Marshal(acceptanceTransitionRequest{
		PlanVersion: tests.PlanVersion, Status: status, Note: strings.TrimSpace(note),
		ImplementationCommitID: tests.ImplementationCommitID,
	})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf(
		"%s/api/v1/projects/%s/features/%s/acceptance-tests/%s/transitions",
		base, url.PathEscape(projectID), url.PathEscape(featureID), url.PathEscape(testID),
	)
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	digest := sha256.Sum256([]byte(featureID + "\x00" + fmt.Sprint(tests.PlanVersion) + "\x00" + testID + "\x00" + string(status) + "\x00" + strings.TrimSpace(note) + "\x00" + tests.ImplementationCommitID))
	request.Header.Set("Idempotency-Key", "artifact-"+hex.EncodeToString(digest[:]))
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("update acceptance test: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
		return fmt.Errorf("update acceptance test: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	updated := artifactResponse{}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(&updated); err != nil {
		return fmt.Errorf("decode updated acceptance tests: %w", err)
	}
	_, err = fmt.Fprintf(output, "acceptance tests revision %d: %s %s\n", updated.Revision, testID, status)
	return err
}

func transition(
	client *http.Client,
	base, projectID, featureID, stepID string,
	status featureartifact.StepStatus,
	commitID string,
	output io.Writer,
) error {
	artifact, err := getPlan(client, base, projectID, featureID)
	if err != nil {
		return err
	}
	plan := featureartifact.ImplementationPlan{}
	if err := json.Unmarshal(artifact.Document, &plan); err != nil {
		return fmt.Errorf("decode implementation plan: %w", err)
	}
	body, err := json.Marshal(transitionRequest{
		PlanVersion: plan.PlanVersion, Status: status, CommitID: strings.TrimSpace(commitID),
	})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf(
		"%s/api/v1/projects/%s/features/%s/implementation-plan/steps/%s/transitions",
		base, url.PathEscape(projectID), url.PathEscape(featureID), url.PathEscape(stepID),
	)
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	digest := sha256.Sum256([]byte(featureID + "\x00" + fmt.Sprint(plan.PlanVersion) + "\x00" + stepID + "\x00" + string(status) + "\x00" + strings.TrimSpace(commitID)))
	request.Header.Set("Idempotency-Key", "artifact-"+hex.EncodeToString(digest[:]))
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("update implementation plan: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
		return fmt.Errorf("update implementation plan: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	updated := artifactResponse{}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(&updated); err != nil {
		return fmt.Errorf("decode updated implementation plan: %w", err)
	}
	_, err = fmt.Fprintf(output, "implementation plan revision %d: %s %s\n", updated.Revision, stepID, status)
	return err
}

func getPlan(client *http.Client, base, projectID, featureID string) (artifactResponse, error) {
	return getArtifact(client, base, projectID, featureID, featureartifact.KindImplementationPlan)
}

func getArtifact(client *http.Client, base, projectID, featureID string, kind featureartifact.Kind) (artifactResponse, error) {
	endpoint := fmt.Sprintf(
		"%s/api/v1/projects/%s/features/%s/artifacts/%s",
		base, url.PathEscape(projectID), url.PathEscape(featureID), kind,
	)
	response, err := client.Get(endpoint)
	if err != nil {
		return artifactResponse{}, fmt.Errorf("get %s: %w", kind, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
		return artifactResponse{}, fmt.Errorf("get %s: HTTP %d: %s", kind, response.StatusCode, strings.TrimSpace(string(message)))
	}
	artifact := artifactResponse{}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256*1024)).Decode(&artifact); err != nil {
		return artifactResponse{}, fmt.Errorf("decode %s: %w", kind, err)
	}
	if artifact.Revision < 1 || len(artifact.Document) == 0 {
		return artifactResponse{}, errors.New("feature artifact response is incomplete")
	}
	return artifact, nil
}

func environment(getenv func(string) string) (string, string, string, error) {
	base := strings.TrimRight(strings.TrimSpace(getenv("COMMITARIUM_COORDINATOR_URL")), "/")
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", "", errors.New("the worker did not provide a safe coordinator URL")
	}
	projectID := strings.TrimSpace(getenv("COMMITARIUM_PROJECT_ID"))
	featureID := strings.TrimSpace(getenv("COMMITARIUM_FEATURE_ID"))
	if projectID == "" || featureID == "" || strings.ContainsAny(projectID+featureID, "/\\\x00") {
		return "", "", "", errors.New("the worker did not provide valid project and feature identity")
	}
	return base, projectID, featureID, nil
}

func usage() error {
	return errors.New("usage: commitarium-artifact plan show | plan start <step-id> | plan complete <step-id> <commit-id> | acceptance show | acceptance start <test-id> | acceptance pass <test-id> | acceptance fail <test-id> <note> | acceptance not-applicable <test-id> <note>")
}
