package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"k8s.io/test-infra/prow/git/v2"

	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	prowapi "k8s.io/test-infra/prow/apis/prowjobs/v1"
	v1 "k8s.io/test-infra/prow/client/clientset/versioned/typed/prowjobs/v1"
	"k8s.io/test-infra/prow/config"
	"k8s.io/test-infra/prow/github"
	"k8s.io/test-infra/prow/pjutil"
	"k8s.io/test-infra/prow/pluginhelp"
	"k8s.io/test-infra/prow/plugins/trigger"
	ctrl "sigs.k8s.io/controller-runtime"
)

func helpProvider(_ []config.OrgRepo) (*pluginhelp.PluginHelp, error) {
	pluginHelp := &pluginhelp.PluginHelp{
		Description: `The draft plugin is used to trigger job when draft PR is created or convert existing PR to draft PR. Useful when you want to trigger specific jobs when PR is in draft`,
	}
	return pluginHelp, nil
}

type server struct {
	tokenGenerator func() []byte
	botUser        *github.UserData
	email          string
	gc             git.ClientFactory
	ghc            github.Client
	log            *logrus.Entry
	repos          []github.Repo
	prowconfig     string
	prowjob        string
	regex          string
	ns             string
}

type githubClient interface {
	GetRef(org, repo, ref string) (string, error)
}

type prowJobClient interface {
	Create(context.Context, *prowapi.ProwJob, metav1.CreateOptions) (*prowapi.ProwJob, error)
	List(ctx context.Context, opts metav1.ListOptions) (*prowapi.ProwJobList, error)
	Update(context.Context, *prowapi.ProwJob, metav1.UpdateOptions) (*prowapi.ProwJob, error)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logrus.Info("inside http server")
	eventType, eventGUID, payload, ok, _ := github.ValidateWebhook(w, r, s.tokenGenerator)
	if !ok {
		return
	}
	logrus.Info(w, "Event received. Have a nice day.")
	if err := s.handleEvent(eventType, eventGUID, payload); err != nil {
		logrus.WithError(err).Error("Error parsing event.")
	}
}

func (s *server) handleEvent(eventType, eventGUID string, payload []byte) error {
	var c *config.Config
	var baseSHA string
	var p = getProwClient()
	c, err := config.Load(s.prowconfig, s.prowjob, nil, "")
	if err != nil {
		logrus.Info(err)
	}
	var pr github.PullRequestEvent
	if err := json.Unmarshal(payload, &pr); err != nil {
		return err
	}
	org, repo, author, ref := orgRepoAuthor(pr.PullRequest)
	baseSHAGetter := func() (string, error) {
		var err error
		baseSHA, err = githubClient.GetRef(s.ghc, org, repo, "heads/"+ref)
		if err != nil {
			return "", fmt.Errorf("failed to get baseSHA: %w", err)
		}
		return baseSHA, nil
	}
	headSHAGetter := func() (string, error) {
		return pr.PullRequest.Head.SHA, nil
	}
	presubmits, err := c.GetPresubmits(s.gc, org+"/"+repo, baseSHAGetter, headSHAGetter)
	if err != nil {
		return fmt.Errorf("server not responding %s", err.Error())
	}
	if len(presubmits) == 0 {
		return nil
	}
	trustedResponse, err := trigger.TrustedUser(s.ghc, true, pr.Repo.Owner.Login, author, org, repo)
	member := trustedResponse.IsTrusted
	if err != nil {
		return fmt.Errorf("could not check membership: %s", err)
	}
	if member {
		switch pr.Action {
		case github.PullRequestActionOpened, github.PullRequestActionSynchronize, github.PullRequestActionConvertedToDraft:
			logrus.WithFields(logrus.Fields{
				github.OrgLogField: org,
				github.RepoLogField: repo,
				github.PrLogField: pr.Number,
				"action": pr.Action,
				"author": author,
				"SHA": pr.PullRequest.Head.SHA,
			}).Info("PR info")
			if pr.PullRequest.Draft {
				var jobs []config.Presubmit
				for _, job := range presubmits {
					match, _ := regexp.MatchString(s.regex, job.Name)
					if match {
						jobs = append(jobs, job)
					}
				}
				var errors []error
				for _, job := range jobs {
					pjob := map[string][]config.Presubmit{
						job.Name: {job},
					}
					err := c.JobConfig.SetPresubmits(pjob)
					if err != nil {
						return fmt.Errorf("error generating presubmit job",err)
					}
					logrus.Infof("Starting %s build.", job.Name)
					pj := pjutil.NewPresubmit(pr.PullRequest, baseSHA, job, eventGUID, nil)
					logrus.WithFields(pjutil.ProwJobFields(&pj)).Info("Creating a new prowjob.")
					if err := createWithRetry(context.Background(), p.ProwJobs(s.ns), &pj, time.Millisecond); err != nil {
						logrus.WithError(err).Error("Failed to create prowjob.")
						errors = append(errors, err)
					}
				}
				logrus.Error(errors)
			}
		default:
			logrus.Debugf("skipping event of type %q", eventType)
		}
	}
	return nil
}

func orgRepoAuthor(pr github.PullRequest) (string, string, string, string) {
	org := pr.Base.Repo.Owner.Login
	repo := pr.Base.Repo.Name
	author := pr.User.Login
	ref := pr.Base.Ref
	return org, repo, author, ref
}

// createWithRetry will retry the creation of a ProwJob. The Name must be set, otherwise we might end up creating it multiple times
// if one Create request throws error but succeeds under the hood.
func createWithRetry(ctx context.Context, client prowJobClient, pj *prowapi.ProwJob, millisecondOverride ...time.Duration) error {
	millisecond := time.Millisecond
	if len(millisecondOverride) == 1 {
		millisecond = millisecondOverride[0]
	}

	var errs []error
	if err := wait.ExponentialBackoff(wait.Backoff{Duration: 250 * millisecond, Factor: 2.0, Jitter: 0.1, Steps: 8}, func() (bool, error) {
		_, err := client.Create(ctx, pj, metav1.CreateOptions{})
		if err != nil {
			// Can happen if a previous request was successful but returned an error
			if apierrors.IsAlreadyExists(err) {
				return true, nil
			}
			// Store and swallow errors, if we end up timing out we will return all of them
			errs = append(errs, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		if err != wait.ErrWaitTimeout {
			return err
		}
		return utilerrors.NewAggregate(errs)
	}

	return nil
}

func getrestClient() *rest.Config {
	config := ctrl.GetConfigOrDie()
	return config
}

func getProwClient() *v1.ProwV1Client {
	return v1.NewForConfigOrDie(getrestClient())
}
