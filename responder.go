package responder

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"github.com/rs/zerolog/log"

	"github.com/google/go-github/v20/github"
	"github.com/justinas/alice"
	"github.com/mholt/certmagic"
	uuid "github.com/satori/go.uuid"
	"golang.org/x/oauth2"
)

const (
	ghtokName = "GITHUB_TOKEN"
)

// Responder -
type Responder struct {
	ghclient    *github.Client
	secret      string
	owner, repo string
	events      []string
	callbackURL string
	actions     []HookHandler
	domain      string
}

// New -
func New(repo, domain string) (*Responder, error) {
	// init repo/owner
	if repo == "" {
		return nil, errors.New("must provide repo")
	}
	repoParts := strings.SplitN(repo, "/", 2)
	if len(repoParts) != 2 {
		return nil, errors.Errorf("invalid repo %s - need 'owner/repo' form", repo)
	}

	// init callback URL
	callbackURL := buildCallbackURL(domain)

	// set random secret
	secret := fmt.Sprintf("%x", rand.Int63())

	token := os.Getenv(ghtokName)
	if token == "" {
		return nil, errors.Errorf("GitHub API token missing - must set %s", ghtokName)
	}
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	hc := &http.Client{Transport: &oauth2.Transport{Source: ts}}
	client := github.NewClient(hc)

	return &Responder{
		ghclient:    client,
		secret:      secret,
		owner:       repoParts[0],
		repo:        repoParts[1],
		domain:      domain,
		callbackURL: callbackURL,
	}, nil
}

func buildCallbackURL(domain string) string {
	u := uuid.NewV4()
	var scheme string
	if tlsDisabled() {
		scheme = "http://"
	} else {
		scheme = "https://"
	}
	return scheme + domain + "/gh-callback/" + u.String()
}

// Register a new webhook with repo. A cleanup function is returned when the hook
// is successfully registered - this function must be called (usually deferred),
// otherwise invalid webhooks will be left behind.
func (r *Responder) Register(ctx context.Context, events []string, actions ...HookHandler) (func(), error) {
	hook := &github.Hook{
		Events: events,
		Config: map[string]interface{}{
			"url":          r.callbackURL,
			"content_type": "json",
			"secret":       r.secret,
		},
	}
	hook, resp, err := r.ghclient.Repositories.CreateHook(ctx, r.owner, r.repo, hook)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create hook")
	}
	if resp.StatusCode > 299 {
		return nil, errors.Errorf("request failed with %s", resp.Status)
	}

	id := hook.GetID()
	log.Info().
		Str("hook_url", hook.GetURL()).
		Int64("hook_id", id).
		Str("callback", r.callbackURL).
		Msg("Registered WebHook")

	r.actions = actions

	unregister := func() {
		log := log.With().Int64("hook_id", id).Logger()
		log.Info().Msg("Cleaning up webhook")
		_, err := r.ghclient.Repositories.DeleteHook(ctx, r.owner, r.repo, id)
		if err != nil {
			err = errors.Wrap(err, "failed to delete webhook")
			log.Error().Err(err).Msg("failed to delete webhook")
		}
	}
	return unregister, nil
}

// Listen for webhooks
func (r *Responder) Listen(ctx context.Context) {
	initMetrics()

	// now listen for events
	c := alice.New(hlog.NewHandler(log.Logger))
	c = c.Append(
		hlog.UserAgentHandler("user_agent"),
		hlog.RefererHandler("referer"),
		hlog.MethodHandler("method"),
		hlog.URLHandler("url"),
		hlog.RemoteAddrHandler("remoteAddr"),
	)
	c = c.Append(hlog.AccessHandler(func(r *http.Request, status, size int, duration time.Duration) {
		eventType := github.WebHookType(r)
		deliveryID := github.DeliveryID(r)
		l := zerolog.DebugLevel
		if status > 399 {
			l = zerolog.WarnLevel
		}
		hlog.FromRequest(r).WithLevel(l).
			Int("status", status).
			Int("size", size).
			Dur("duration", duration).
			Str("eventType", eventType).
			Str("deliveryID", deliveryID).
			Msgf("%s %s - %d", r.Method, r.URL, status)
	}))

	http.Handle("/metrics", c.Append(filterByIP).Extend(instrumentHTTP("metrics")).Then(promhttp.Handler()))
	http.Handle(getPath(r.callbackURL), c.Extend(instrumentHTTP("callback")).Then(r))
	http.Handle("/", c.Extend(instrumentHTTP("default")).ThenFunc(denyHandler))

	if tlsDisabled() {
		go func() {
			log.Info().Int("port", certmagic.HTTPPort).Msg("Listening for webhook callbacks")
			port := strconv.Itoa(certmagic.HTTPPort)
			err := http.ListenAndServe(":"+port, nil)
			log.Error().Err(err).Msg("")
		}()
	}

	go func() {
		log.Info().Int("port", certmagic.HTTPSPort).Msg("Listening for webhook callbacks")
		err := certmagic.HTTPS([]string{r.domain}, nil)
		log.Error().Err(err).Msg("listening with certmagic")
	}()

	return
}

// RegisterAndListen - unlike calling `Register` and `Listen` separately, this
// will block while waiting for the context to be cancelled.
func (r *Responder) RegisterAndListen(ctx context.Context, events []string, actions ...HookHandler) error {
	cleanup, err := r.Register(ctx, events, actions...)
	if err != nil {
		return err
	}
	defer cleanup()

	r.Listen(ctx)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	select {
	case s := <-c:
		log.Debug().
			Str("signal", s.String()).
			Msg("shutting down gracefully...")
	case <-ctx.Done():
		err = ctx.Err()
		log.Error().
			Err(err).
			Msg("context cancelled")
	}
	return err
}

func tlsDisabled() bool {
	disableTLS, err := strconv.ParseBool(os.Getenv("TLS_DISABLE"))
	if err != nil {
		return false
	}
	return disableTLS
}

func getPath(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	if parsed.Path != "" {
		return parsed.Path
	}
	return u
}

func (r *Responder) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	log := *hlog.FromRequest(req)
	payload, err := github.ValidatePayload(req, []byte(r.secret))
	if err != nil {
		log.Error().Err(err).
			Msg("invalid payload")
		http.Error(resp, err.Error(), http.StatusBadRequest)
		return
	}

	eventType := github.WebHookType(req)
	deliveryID := github.DeliveryID(req)
	log = log.With().
		Str("eventType", eventType).
		Str("deliveryID", deliveryID).Logger()
	log.Info().Msg("Incoming request")
	if eventType == "ping" {
		event, err := github.ParseWebHook(eventType, payload)
		if err != nil {
			log.Error().Err(err).Msg("failed to parse payload")
			http.Error(resp, err.Error(), http.StatusBadRequest)
			return
		}
		ping, ok := event.(*github.PingEvent)
		if !ok {
			http.Error(resp, fmt.Sprintf("wrong event type %T", event), http.StatusBadRequest)
			return
		}
		_, err = resp.Write([]byte(*ping.Zen))
		if err != nil {
			log.Error().Err(err).Msg("failed to write response")
		}
		return
	}

	ctx := log.WithContext(req.Context())
	for _, a := range r.actions {
		go a(ctx, eventType, deliveryID, payload)
	}

	resp.WriteHeader(http.StatusNoContent)
}

func denyHandler(resp http.ResponseWriter, req *http.Request) {
	resp.WriteHeader(http.StatusNotFound)
}

// HookHandler - A function that will be executed by the callback.
//
// Payload is provided as []byte, and can be parsed with github.ParseWebHook if desired
type HookHandler func(ctx context.Context, eventType, deliveryID string, payload []byte)
