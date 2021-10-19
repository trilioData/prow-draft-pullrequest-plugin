package main

import (
	"flag"
	"net/http"
	"os"
	"strconv"
	"time"

	"k8s.io/test-infra/pkg/flagutil"
	"k8s.io/test-infra/prow/git/v2"

	"github.com/sirupsen/logrus"
	"k8s.io/test-infra/prow/interrupts"
	"k8s.io/test-infra/prow/logrusutil"
	"k8s.io/test-infra/prow/pjutil"

	"k8s.io/test-infra/prow/config/secret"
	prowflagutil "k8s.io/test-infra/prow/flagutil"
	configflagutil "k8s.io/test-infra/prow/flagutil/config"
	"k8s.io/test-infra/prow/pluginhelp/externalplugins"
)

const pluginName = "draft-plugin"

type options struct {
	port int

	config                 configflagutil.ConfigOptions
	dryRun                 bool
	github                 prowflagutil.GitHubOptions
	instrumentationOptions prowflagutil.InstrumentationOptions
	prowURL                string
	prowconfig             string
	prowjob                string
	jobregex               string
	ns                     string

	webhookSecretFile string
}

func (o *options) Validate() error {
	return nil
}

func gatherOptions() options {
	o := options{config: configflagutil.ConfigOptions{ConfigPath: "./config.yaml"}}
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	fs.IntVar(&o.port, "port", 8888, "Port to listen on.")
	fs.BoolVar(&o.dryRun, "dry-run", false, "Dry run for testing. Uses API tokens but does not mutate.")
	fs.StringVar(&o.webhookSecretFile, "hmac-secret-file", "/etc/hmac", "Path to the file containing GitHub HMAC secret.")
	fs.StringVar(&o.prowconfig, "config-path", "/etc/config/config.yaml", "Prow configuration file path")
	fs.StringVar(&o.prowjob, "job-path", "/etc/job-config", "Prow job yaml's path")
	fs.StringVar(&o.jobregex, "regex", "", "regex of job name")
	fs.StringVar(&o.ns, "namespace", "default", "kubernetes namespace")
	for _, group := range []flagutil.OptionGroup{&o.github} {
		group.AddFlags(fs)
	}
	fs.Parse(os.Args[1:])
	return o
}

func main() {
	o := gatherOptions()
	if err := o.Validate(); err != nil {
		logrus.Fatalf("Invalid options: %v", err)
	}

	logrusutil.ComponentInit()
	log := logrus.StandardLogger().WithField("plugin", pluginName)

	if err := secret.Add(o.webhookSecretFile); err != nil {
		logrus.WithError(err).Fatal("Error starting secrets agent.")
	}

	gitHubClient, err := o.github.GitHubClient(o.dryRun)
	if err != nil {
		logrus.WithError(err).Fatal("Error getting GitHub client.")
	}
	gitClient, err := o.github.GitClient(o.dryRun)
	if err != nil {
		logrus.WithError(err).Fatal("Error getting Git client.")
	}
	interrupts.OnInterrupt(func() {
		if err := gitClient.Clean(); err != nil {
			logrus.WithError(err).Error("Could not clean up git client cache.")
		}
	})

	email, err := gitHubClient.Email()
	if err != nil {
		log.WithError(err).Fatal("Error getting bot e-mail.")
	}

	botUser, err := gitHubClient.BotUser()
	if err != nil {
		logrus.WithError(err).Fatal("Error getting bot name.")
	}
	repos, err := gitHubClient.GetRepos(botUser.Login, true)
	if err != nil {
		log.WithError(err).Fatal("Error listing bot repositories.")
	}
	serv := &server{
		tokenGenerator: secret.GetTokenGenerator(o.webhookSecretFile),
		botUser:        botUser,
		email:          email,
		gc:             git.ClientFactoryFrom(gitClient),
		ghc:            gitHubClient,
		log:            log,
		repos:          repos,
		prowconfig:     o.prowconfig,
		prowjob:        o.prowjob,
		regex:          o.jobregex,
		ns:             o.ns,
	}

	health := pjutil.NewHealthOnPort(o.instrumentationOptions.HealthPort)
	health.ServeReady()

	mux := http.NewServeMux()
	mux.Handle("/", serv)
	externalplugins.ServeExternalPluginHelp(mux, log, helpProvider)
	logrus.Info("starting server")
	httpServer := &http.Server{Addr: ":" + strconv.Itoa(o.port), Handler: mux}
	defer interrupts.WaitForGracefulShutdown()
	interrupts.ListenAndServe(httpServer, 5*time.Second)
}
