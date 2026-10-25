package githubpr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asierhr/featureProof/internal/githubcontext"
)

// HTTPDoer is the only transport capability required by the REST adapter.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// RESTConfig fixes the trusted API origin/version, app identity, token source, and response bounds.
type RESTConfig struct {
	BaseURL, APIVersion string
	AppID               int64
	Tokens              CredentialSource
	HTTP                HTTPDoer
	Clock               Clock
	MaxResponse         int64
	MaxPages            int
	MaxChangedPaths     int
}

// repositorySlug accepts only GitHub owner/repository path segments before constructing the files endpoint.
var repositorySlug = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,99})$`)

// pullRequestDTO is the strict structural subset needed to bind metadata, files and the exact base repository route.
type pullRequestDTO struct {
	Number int64  `json:"number"`
	State  string `json:"state"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	User   struct {
		ID int64 `json:"id"`
	} `json:"user"`
	Head struct {
		SHA  string `json:"sha"`
		Repo struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		SHA  string `json:"sha"`
		Repo struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

// ContextPermissions returns the read-only installation scope used by PR context acquisition.
func ContextPermissions() PermissionSet {
	return PermissionSet{"metadata": "read", "pull_requests": "read"}
}

// Metadata reads the exact neutral PR fields required by githubcontext without exposing DTO URLs.
func (a *RESTAdapter) Metadata(ctx context.Context, installationID, repositoryID, number int64, maxResponseBytes int) (githubcontext.RemoteMetadata, error) {
	var dto pullRequestDTO
	resource := fmt.Sprintf("/repositories/%d/pulls/%d", repositoryID, number)
	if _, err := a.requestHeadersWithPermissionsLimit(ctx, installationID, repositoryID, ContextPermissions(), maxResponseBytes, http.MethodGet, resource, nil, &dto); err != nil {
		return githubcontext.RemoteMetadata{}, mapContextError(ctx, err, a.config.Clock.Now())
	}
	if ctx.Err() != nil {
		return githubcontext.RemoteMetadata{}, mapContextError(ctx, ctx.Err(), a.config.Clock.Now())
	}
	if dto.Number != number || dto.Base.Repo.ID != repositoryID {
		return githubcontext.RemoteMetadata{}, &githubcontext.Error{Category: githubcontext.CategoryIntegrity, Stage: "metadata"}
	}
	return githubcontext.RemoteMetadata{InstallationID: installationID, RepositoryID: repositoryID, Number: dto.Number, State: dto.State, HeadOID: dto.Head.SHA, BaseOID: dto.Base.SHA, Title: dto.Title, Body: dto.Body}, nil
}

// Files reads one locally addressed bounded page and never follows provider URLs.
func (a *RESTAdapter) Files(ctx context.Context, installationID, repositoryID, number int64, pageNumber, perPage, maxResponseBytes int) (githubcontext.FilePage, error) {
	if pageNumber <= 0 || perPage <= 0 || perPage > 100 || maxResponseBytes <= 0 {
		return githubcontext.FilePage{}, &githubcontext.Error{Category: githubcontext.CategoryInvalid, Stage: "files_page"}
	}
	var values []struct {
		Filename         string  `json:"filename"`
		PreviousFilename string  `json:"previous_filename"`
		Status           string  `json:"status"`
		Additions        int     `json:"additions"`
		Deletions        int     `json:"deletions"`
		Changes          int     `json:"changes"`
		Patch            *string `json:"patch"`
	}
	resource := fmt.Sprintf("/repositories/%d/pulls/%d/files?per_page=%d&page=%d", repositoryID, number, perPage, pageNumber)
	header, err := a.requestHeadersWithPermissionsLimit(ctx, installationID, repositoryID, ContextPermissions(), maxResponseBytes, http.MethodGet, resource, nil, &values)
	if err != nil {
		return githubcontext.FilePage{}, mapContextError(ctx, err, a.config.Clock.Now())
	}
	if ctx.Err() != nil {
		return githubcontext.FilePage{}, mapContextError(ctx, ctx.Err(), a.config.Clock.Now())
	}
	if len(values) > perPage {
		return githubcontext.FilePage{}, &githubcontext.Error{Category: githubcontext.CategoryIntegrity, Stage: "files_page"}
	}
	page := githubcontext.FilePage{Files: make([]githubcontext.RemoteFile, len(values))}
	for index, value := range values {
		page.Files[index] = githubcontext.RemoteFile{Path: value.Filename, PreviousPath: value.PreviousFilename, Status: githubcontext.FileStatus(value.Status), Additions: value.Additions, Deletions: value.Deletions, Changes: value.Changes, Patch: value.Patch}
	}
	expectedPath := strings.TrimSuffix(a.base.Path, "/") + fmt.Sprintf("/repositories/%d/pulls/%d/files", repositoryID, number)
	page.Next, err = strictNextLink(header.Get("Link"), a.base, expectedPath, pageNumber, perPage)
	if ctx.Err() != nil {
		return githubcontext.FilePage{}, mapContextError(ctx, ctx.Err(), a.config.Clock.Now())
	}
	if err != nil || page.Next && len(page.Files) == 0 {
		return githubcontext.FilePage{}, &githubcontext.Error{Category: githubcontext.CategoryIntegrity, Stage: "files_pagination"}
	}
	return page, nil
}

// mapContextError translates the existing adapter taxonomy without leaking provider responses.
func mapContextError(ctx context.Context, err error, now time.Time) error {
	if ctx.Err() != nil {
		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		}
		category := githubcontext.CategoryCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(cause, context.DeadlineExceeded) {
			category = githubcontext.CategoryTimeout
		}
		return &githubcontext.Error{Category: category, Stage: "github", Cause: cause}
	}
	var typed *Error
	if !errors.As(err, &typed) {
		return &githubcontext.Error{Category: githubcontext.CategoryInternal, Stage: "github", Cause: err}
	}
	category := githubcontext.CategoryUnauthorized
	switch typed.Code {
	case CodeInvalid:
		category = githubcontext.CategoryInvalid
	case CodeConflict:
		category = githubcontext.CategoryConflict
	case CodeIntegrity:
		category = githubcontext.CategoryIntegrity
	case CodeTransient:
		category = githubcontext.CategoryTransient
	case CodeRateLimited:
		category = githubcontext.CategoryRateLimited
	case CodeCancelled:
		category = githubcontext.CategoryCancelled
	case CodeInternal:
		category = githubcontext.CategoryInternal
	}
	retryAt := typed.ResetAt
	if retryAt.IsZero() && typed.RetryAfter > 0 {
		retryAt = now.Add(typed.RetryAfter)
	}
	return &githubcontext.Error{Category: category, Stage: "github", Cause: err, RetryAt: retryAt}
}

// RESTAdapter translates GitHub REST DTOs into the package-owned gateway contract.
type RESTAdapter struct {
	config RESTConfig
	base   *url.URL
}

// NewRESTAdapter validates transport configuration and forbids URL substitution outside HTTPS.
func NewRESTAdapter(config RESTConfig) (*RESTAdapter, error) {
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || config.APIVersion == "" || config.AppID <= 0 || config.Tokens == nil || config.HTTP == nil || config.Clock == nil || config.MaxResponse <= 0 || config.MaxPages <= 0 || config.MaxChangedPaths <= 0 {
		return nil, &Error{Code: CodeInvalid, Operation: "rest_config"}
	}
	return &RESTAdapter{config: config, base: base}, nil
}

// PullRequest reads metadata, every bounded changed-file page, then metadata again so head and paths form one coherent observation.
func (a *RESTAdapter) PullRequest(ctx context.Context, installationID, repositoryID, number int64) (PullRequestSnapshot, error) {
	if installationID <= 0 || repositoryID <= 0 || number <= 0 {
		return PullRequestSnapshot{}, &Error{Code: CodeInvalid, Operation: "pull_request_identity"}
	}
	first, owner, repository, err := a.readPullRequestMetadata(ctx, installationID, repositoryID, number)
	if err != nil {
		return PullRequestSnapshot{}, err
	}
	changedPaths, err := a.readChangedPaths(ctx, installationID, repositoryID, owner, repository, number)
	if err != nil {
		return PullRequestSnapshot{}, err
	}
	second, secondOwner, secondRepository, err := a.readPullRequestMetadata(ctx, installationID, repositoryID, number)
	if err != nil {
		return PullRequestSnapshot{}, err
	}
	if first.Fingerprint != second.Fingerprint || owner != secondOwner || repository != secondRepository {
		return PullRequestSnapshot{}, &Error{Code: CodeConflict, Operation: "pull_request_changed_during_read"}
	}
	second.ChangedPaths = changedPaths
	return SealSnapshot(second)
}

// readPullRequestMetadata validates the numeric identity and extracts safe owner/repository segments for the files endpoint.
func (a *RESTAdapter) readPullRequestMetadata(ctx context.Context, installationID, repositoryID, number int64) (PullRequestSnapshot, string, string, error) {
	var dto pullRequestDTO
	if err := a.request(ctx, installationID, repositoryID, http.MethodGet, fmt.Sprintf("/repositories/%d/pulls/%d", repositoryID, number), nil, &dto); err != nil {
		return PullRequestSnapshot{}, "", "", err
	}
	parts := strings.Split(dto.Base.Repo.FullName, "/")
	if len(parts) != 2 || !repositorySlug.MatchString(parts[0]) || !repositorySlug.MatchString(parts[1]) || dto.Base.Repo.ID != repositoryID || dto.Number != number {
		return PullRequestSnapshot{}, "", "", &Error{Code: CodeIntegrity, Operation: "pull_request_metadata"}
	}
	snapshot, err := SealSnapshot(PullRequestSnapshot{Version: SchemaVersion, InstallationID: installationID, RepositoryID: dto.Base.Repo.ID, Number: dto.Number, AuthorID: dto.User.ID, State: dto.State, HeadOID: dto.Head.SHA, HeadRepositoryID: dto.Head.Repo.ID, Fork: dto.Head.Repo.ID != dto.Base.Repo.ID})
	if err != nil {
		return PullRequestSnapshot{}, "", "", err
	}
	return snapshot, parts[0], parts[1], nil
}

// readChangedPaths consumes the complete bounded files collection and rejects noncanonical or duplicate authority.
func (a *RESTAdapter) readChangedPaths(ctx context.Context, installationID, repositoryID int64, owner, repository string, number int64) ([]string, error) {
	values := make([]string, 0)
	seen := make(map[string]bool)
	for pageNumber := 1; pageNumber <= a.config.MaxPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, &Error{Code: CodeCancelled, Operation: "pull_request_files"}
		}
		var pageValues []struct {
			Filename string `json:"filename"`
		}
		resource := fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100&page=%d", url.PathEscape(owner), url.PathEscape(repository), number, pageNumber)
		header, err := a.requestHeaders(ctx, installationID, repositoryID, http.MethodGet, resource, nil, &pageValues)
		if err != nil {
			return nil, err
		}
		if len(pageValues) > 100 {
			return nil, &Error{Code: CodeIntegrity, Operation: "pull_request_files_page"}
		}
		for _, value := range pageValues {
			canonical, valid := canonicalGitHubChangedPath(value.Filename)
			if !valid || seen[canonical] {
				return nil, &Error{Code: CodeIntegrity, Operation: "pull_request_files_path"}
			}
			if len(values) >= a.config.MaxChangedPaths {
				return nil, &Error{Code: CodeConflict, Operation: "pull_request_files_limit"}
			}
			seen[canonical] = true
			values = append(values, canonical)
		}
		expectedPath := strings.TrimSuffix(a.base.Path, "/") + fmt.Sprintf("/repos/%s/%s/pulls/%d/files", url.PathEscape(owner), url.PathEscape(repository), number)
		next, linkErr := strictNextLink(header.Get("Link"), a.base, expectedPath, pageNumber, 100)
		if linkErr != nil || next && len(pageValues) == 0 {
			return nil, &Error{Code: CodeIntegrity, Operation: "pull_request_files_pagination"}
		}
		if !next {
			sort.Strings(values)
			return values, nil
		}
	}
	return nil, &Error{Code: CodeConflict, Operation: "pull_request_files_pagination_limit"}
}

// canonicalGitHubChangedPath accepts only already-canonical repository-relative slash paths.
func canonicalGitHubChangedPath(value string) (string, bool) {
	return githubcontext.CanonicalRepositoryPath(value)
}

// strictNextLink validates the bounded pagination signal while never following a remote URL from the response.
func strictNextLink(value string, base *url.URL, expectedPath string, currentPage, perPage int) (bool, error) {
	if value == "" {
		return false, nil
	}
	if base == nil || currentPage <= 0 || perPage <= 0 || expectedPath == "" {
		return false, &Error{Code: CodeIntegrity, Operation: "pull_request_files_link"}
	}
	found := false
	relations := make(map[string]bool)
	for _, raw := range strings.Split(value, ",") {
		part := strings.TrimSpace(raw)
		segments := strings.Split(part, ";")
		if len(segments) != 2 {
			return false, &Error{Code: CodeIntegrity, Operation: "pull_request_files_link"}
		}
		target := strings.TrimSpace(segments[0])
		relation := strings.TrimSpace(segments[1])
		if len(target) < 3 || target[0] != '<' || target[len(target)-1] != '>' || !strings.HasPrefix(relation, `rel="`) || !strings.HasSuffix(relation, `"`) {
			return false, &Error{Code: CodeIntegrity, Operation: "pull_request_files_link"}
		}
		parsed, err := url.Parse(target[1 : len(target)-1])
		if err != nil || parsed.Scheme != base.Scheme || !strings.EqualFold(parsed.Host, base.Host) || parsed.User != nil || parsed.Fragment != "" || parsed.Path != expectedPath {
			return false, &Error{Code: CodeIntegrity, Operation: "pull_request_files_link"}
		}
		name := strings.TrimSuffix(strings.TrimPrefix(relation, `rel="`), `"`)
		if relations[name] || name != "next" && name != "prev" && name != "first" && name != "last" {
			return false, &Error{Code: CodeIntegrity, Operation: "pull_request_files_link"}
		}
		query := parsed.Query()
		if len(query) != 2 || len(query["page"]) != 1 || len(query["per_page"]) != 1 || query.Get("per_page") != strconv.Itoa(perPage) {
			return false, &Error{Code: CodeIntegrity, Operation: "pull_request_files_link"}
		}
		pageNumber, pageErr := strconv.Atoi(query.Get("page"))
		if pageErr != nil || pageNumber <= 0 || name == "next" && pageNumber != currentPage+1 || name == "prev" && (currentPage <= 1 || pageNumber != currentPage-1) || name == "first" && pageNumber != 1 || name == "last" && pageNumber < currentPage {
			return false, &Error{Code: CodeIntegrity, Operation: "pull_request_files_link"}
		}
		relations[name] = true
		if name == "next" {
			found = true
		}
	}
	return found, nil
}

// FindChecks lists only the bounded check collection needed for reconciliation.
func (a *RESTAdapter) FindChecks(ctx context.Context, installationID, repositoryID int64, headOID, externalID string, maxPages int) ([]CheckRecord, error) {
	records := []CheckRecord{}
	for page := 1; page <= maxPages; page++ {
		var dto struct {
			CheckRuns []struct {
				ID         int64  `json:"id"`
				HeadSHA    string `json:"head_sha"`
				ExternalID string `json:"external_id"`
				App        struct {
					ID int64 `json:"id"`
				} `json:"app"`
			} `json:"check_runs"`
		}
		header, err := a.requestHeaders(ctx, installationID, repositoryID, http.MethodGet, fmt.Sprintf("/repositories/%d/commits/%s/check-runs?per_page=100&page=%d", repositoryID, url.PathEscape(headOID), page), nil, &dto)
		if err != nil {
			return nil, err
		}
		for _, v := range dto.CheckRuns {
			records = append(records, CheckRecord{ID: v.ID, AppID: v.App.ID, HeadOID: v.HeadSHA, ExternalID: v.ExternalID})
		}
		if !hasNextLink(header.Get("Link")) {
			return records, nil
		}
	}
	return nil, &Error{Code: CodeConflict, Operation: "check_pagination_limit"}
}

// CreateCheck creates one check for an exact head and external identity.
func (a *RESTAdapter) CreateCheck(ctx context.Context, installationID, repositoryID int64, payload CheckPublication) (CheckRecord, error) {
	var dto struct {
		ID         int64  `json:"id"`
		HeadSHA    string `json:"head_sha"`
		ExternalID string `json:"external_id"`
		App        struct {
			ID int64 `json:"id"`
		} `json:"app"`
	}
	err := a.request(ctx, installationID, repositoryID, http.MethodPost, fmt.Sprintf("/repositories/%d/check-runs", repositoryID), checkDTO(payload), &dto)
	return CheckRecord{ID: dto.ID, AppID: dto.App.ID, HeadOID: dto.HeadSHA, ExternalID: dto.ExternalID}, err
}

// UpdateCheck updates only an already-owned numeric check ID.
func (a *RESTAdapter) UpdateCheck(ctx context.Context, installationID, repositoryID, checkID int64, payload CheckPublication) (CheckRecord, error) {
	var dto struct {
		ID         int64  `json:"id"`
		HeadSHA    string `json:"head_sha"`
		ExternalID string `json:"external_id"`
		App        struct {
			ID int64 `json:"id"`
		} `json:"app"`
	}
	err := a.request(ctx, installationID, repositoryID, http.MethodPatch, fmt.Sprintf("/repositories/%d/check-runs/%d", repositoryID, checkID), checkDTO(payload), &dto)
	return CheckRecord{ID: dto.ID, AppID: dto.App.ID, HeadOID: dto.HeadSHA, ExternalID: dto.ExternalID}, err
}

// GetCheck reads one check so ownership is proven before any update.
func (a *RESTAdapter) GetCheck(ctx context.Context, installationID, repositoryID, checkID int64) (CheckRecord, error) {
	var dto struct {
		ID         int64  `json:"id"`
		HeadSHA    string `json:"head_sha"`
		ExternalID string `json:"external_id"`
		App        struct {
			ID int64 `json:"id"`
		} `json:"app"`
	}
	err := a.request(ctx, installationID, repositoryID, http.MethodGet, fmt.Sprintf("/repositories/%d/check-runs/%d", repositoryID, checkID), nil, &dto)
	return CheckRecord{ID: dto.ID, AppID: dto.App.ID, HeadOID: dto.HeadSHA, ExternalID: dto.ExternalID}, err
}

// FindComments lists a bounded PR comment page for marker and bot reconciliation.
func (a *RESTAdapter) FindComments(ctx context.Context, installationID, repositoryID, number int64, marker string, maxPages int) ([]CommentRecord, error) {
	records := []CommentRecord{}
	for page := 1; page <= maxPages; page++ {
		var dto []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		header, err := a.requestHeaders(ctx, installationID, repositoryID, http.MethodGet, fmt.Sprintf("/repositories/%d/issues/%d/comments?per_page=100&page=%d", repositoryID, number, page), nil, &dto)
		if err != nil {
			return nil, err
		}
		for _, v := range dto {
			if marker == SummaryMarker && hasCanonicalMarker(v.Body) {
				records = append(records, CommentRecord{ID: v.ID, AuthorID: v.User.ID, Body: v.Body})
			}
		}
		if !hasNextLink(header.Get("Link")) {
			return records, nil
		}
	}
	return nil, &Error{Code: CodeConflict, Operation: "comment_pagination_limit"}
}

// CreateComment creates the one versioned summary marker comment.
func (a *RESTAdapter) CreateComment(ctx context.Context, installationID, repositoryID, number int64, body string) (CommentRecord, error) {
	var dto struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	err := a.request(ctx, installationID, repositoryID, http.MethodPost, fmt.Sprintf("/repositories/%d/issues/%d/comments", repositoryID, number), map[string]string{"body": body}, &dto)
	return CommentRecord{ID: dto.ID, AuthorID: dto.User.ID, Body: dto.Body}, err
}

// UpdateComment updates one previously confirmed own comment.
func (a *RESTAdapter) UpdateComment(ctx context.Context, installationID, repositoryID, commentID int64, body string) (CommentRecord, error) {
	var dto struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	err := a.request(ctx, installationID, repositoryID, http.MethodPatch, fmt.Sprintf("/repositories/%d/issues/comments/%d", repositoryID, commentID), map[string]string{"body": body}, &dto)
	return CommentRecord{ID: dto.ID, AuthorID: dto.User.ID, Body: dto.Body}, err
}

// GetComment reads one comment so bot and marker ownership are proven before update.
func (a *RESTAdapter) GetComment(ctx context.Context, installationID, repositoryID, commentID int64) (CommentRecord, error) {
	var dto struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	err := a.request(ctx, installationID, repositoryID, http.MethodGet, fmt.Sprintf("/repositories/%d/issues/comments/%d", repositoryID, commentID), nil, &dto)
	return CommentRecord{ID: dto.ID, AuthorID: dto.User.ID, Body: dto.Body}, err
}

// hasNextLink recognizes GitHub's standard Link relation without trusting its URL for navigation.
func hasNextLink(value string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.Contains(part, `rel="next"`) {
			return true
		}
	}
	return false
}

// checkDTO isolates GitHub field names and output nesting inside the adapter.
func checkDTO(value CheckPublication) any {
	return map[string]any{"name": value.Name, "head_sha": value.HeadOID, "external_id": value.ExternalID, "status": value.Status, "conclusion": emptyNil(value.Conclusion), "details_url": emptyNil(value.DetailsURL), "output": map[string]string{"title": value.Title, "summary": value.Summary}}
}

// emptyNil omits optional GitHub fields instead of sending invalid empty strings.
func emptyNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// request resolves and validates a fresh exact-scope token and classifies bounded REST failures.
func (a *RESTAdapter) request(ctx context.Context, installationID, repositoryID int64, method, resource string, input, output any) error {
	_, err := a.requestHeaders(ctx, installationID, repositoryID, method, resource, input, output)
	return err
}

// requestHeaders performs one bounded authenticated REST exchange and returns safe response metadata for pagination.
func (a *RESTAdapter) requestHeaders(ctx context.Context, installationID, repositoryID int64, method, resource string, input, output any) (http.Header, error) {
	return a.requestHeadersWithPermissions(ctx, installationID, repositoryID, RequiredPermissions(), method, resource, input, output)
}

// requestHeadersWithPermissions confines context acquisition to its smaller read-only token scope.
func (a *RESTAdapter) requestHeadersWithPermissions(ctx context.Context, installationID, repositoryID int64, permissions PermissionSet, method, resource string, input, output any) (http.Header, error) {
	return a.requestHeadersWithPermissionsLimit(ctx, installationID, repositoryID, permissions, int(a.config.MaxResponse), method, resource, input, output)
}

// requestHeadersWithPermissionsLimit applies the narrower policy response budget before decoding a context DTO.
func (a *RESTAdapter) requestHeadersWithPermissionsLimit(ctx context.Context, installationID, repositoryID int64, permissions PermissionSet, maxResponseBytes int, method, resource string, input, output any) (http.Header, error) {
	if maxResponseBytes <= 0 || int64(maxResponseBytes) > a.config.MaxResponse {
		return nil, &Error{Code: CodeInvalid, Operation: "response_limit"}
	}
	token, err := a.config.Tokens.Token(ctx, installationID, repositoryID, permissions)
	if ctx.Err() != nil {
		return nil, &Error{Code: CodeCancelled, Operation: "token"}
	}
	if err != nil {
		var typed *Error
		if asGitHubError(err, &typed) && (typed.Code == CodeTransient || typed.Code == CodeRateLimited || typed.Code == CodeCancelled) {
			return nil, typed
		}
		return nil, &Error{Code: CodeUnauthorized, Operation: "token"}
	}
	if err = ValidateTokenFor(token, repositoryID, permissions, a.config.Clock.Now()); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Operation: "encode"}
	}
	target := *a.base
	target.Path = strings.TrimSuffix(a.base.Path, "/") + resource
	target.RawQuery = ""
	if index := strings.Index(resource, "?"); index >= 0 {
		target.Path = strings.TrimSuffix(a.base.Path, "/") + resource[:index]
		target.RawQuery = resource[index+1:]
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, &Error{Code: CodeInternal, Operation: "request"}
	}
	request.Header.Set("Authorization", "Bearer "+token.Value)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", a.config.APIVersion)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.config.HTTP.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &Error{Code: CodeCancelled, Operation: "http"}
		}
		return nil, &Error{Code: CodeTransient, Operation: "http"}
	}
	defer response.Body.Close()
	if ctx.Err() != nil {
		return nil, &Error{Code: CodeCancelled, Operation: "http"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, classifyHTTP(response, a.config.Clock.Now())
	}
	if output == nil {
		return response.Header, nil
	}
	responseLimit := int64(maxResponseBytes)
	responseRaw, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if ctx.Err() != nil {
		return nil, &Error{Code: CodeCancelled, Operation: "response"}
	}
	if err != nil || int64(len(responseRaw)) > responseLimit || !utf8.Valid(responseRaw) {
		return nil, &Error{Code: CodeIntegrity, Operation: "response"}
	}
	decoder := json.NewDecoder(bytes.NewReader(responseRaw))
	if err := decoder.Decode(output); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &Error{Code: CodeIntegrity, Operation: "response"}
	}
	return response.Header, nil
}

// classifyHTTP translates status and rate-limit headers without retaining remote bodies.
func classifyHTTP(response *http.Response, now time.Time) error {
	typed := &Error{Operation: "github"}
	switch {
	case response.StatusCode == 408 || response.StatusCode >= 500:
		typed.Code = CodeTransient
	case response.StatusCode == 429 || response.StatusCode == 403 && (response.Header.Get("Retry-After") != "" || response.Header.Get("X-RateLimit-Remaining") == "0"):
		typed.Code = CodeRateLimited
	default:
		typed.Code = CodeUnauthorized
	}
	if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
		typed.RetryAfter = time.Duration(seconds) * time.Second
	}
	if response.Header.Get("X-RateLimit-Remaining") == "0" {
		if epoch, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			typed.ResetAt = time.Unix(epoch, 0)
			if !typed.ResetAt.After(now) {
				typed.ResetAt = time.Time{}
			}
		}
	}
	return typed
}

// Compile-time checks ensure the REST adapter exposes only the package-owned contract.
var _ PullRequestGateway = (*RESTAdapter)(nil)

// Compile-time context check keeps read-only acquisition behind the consumer-owned neutral port.
var _ githubcontext.Gateway = (*RESTAdapter)(nil)
