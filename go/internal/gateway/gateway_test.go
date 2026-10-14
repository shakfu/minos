package gateway

// The gateway against a real server and the SDK's own A2A client: alice is the
// gateway's account, bob the worker, demo the developer who opened the room.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"minos/internal/client"
	"minos/internal/testserver"
)

const token = "s3cret"

type fixture struct {
	gateway   *Gateway
	endpoint  string
	agent     *a2aclient.Client
	worker    *client.Client
	developer *client.Client
	room      string
}

func start(t *testing.T, timeout time.Duration) *fixture {
	t.Helper()
	server := testserver.Start(t, 100)

	_, developer, _, err := client.Connect(server.Base, "demo", "demo")
	if err != nil {
		t.Fatalf("the developer cannot connect: %v", err)
	}
	t.Cleanup(developer.Stop)
	room, err := developer.OpenRoom([]client.Principal{
		{Kind: "user", ID: "alice"}, {Kind: "user", ID: "bob"},
	}, "Run", "persisted")
	if err != nil {
		t.Fatalf("cannot open the room: %v", err)
	}

	_, worker, _, err := client.Connect(server.Base, "bob", "bob")
	if err != nil {
		t.Fatalf("the worker cannot connect: %v", err)
	}
	t.Cleanup(worker.Stop)

	f := &fixture{worker: worker, developer: developer, room: room.ID}
	httpServer := httptest.NewUnstartedServer(nil)
	f.endpoint = "http://" + httpServer.Listener.Addr().String()

	f.gateway, err = Open(Config{
		Server: server.Base, User: "alice", Password: "alice",
		Room: room.ID, Worker: "bob",
		Base: f.endpoint, Token: token, Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("the gateway cannot open: %v", err)
	}
	t.Cleanup(f.gateway.Stop)
	httpServer.Config.Handler = f.gateway.Handler()
	httpServer.Start()
	t.Cleanup(httpServer.Close)

	f.agent, err = a2aclient.NewFromCard(context.Background(), f.gateway.Card(),
		a2aclient.WithJSONRPCTransport(&http.Client{Transport: bearer{token}}))
	if err != nil {
		t.Fatalf("cannot make an A2A client: %v", err)
	}
	return f
}

// bearer adds the token to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func text(body string) *a2a.SendMessageRequest {
	return &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(body))}
}

// soon sends immediately and returns the task while it waits for the worker.
func soon(body string) *a2a.SendMessageRequest {
	request := text(body)
	request.Config = &a2a.SendMessageConfig{ReturnImmediately: true}
	return request
}

// heard waits until the room holds body from author, and returns its sequence.
func heard(t *testing.T, c *client.Client, room, author, body string) int64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, message := range c.Log(room) {
			if message.Author == author && message.Body == body {
				return message.Seq
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the room never heard %s say %q", author, body)
	return 0
}

func say(t *testing.T, c *client.Client, room, body string) {
	t.Helper()
	if err := c.Send(room, body); err != nil {
		t.Fatalf("cannot send %q: %v", body, err)
	}
}

func artifactText(t *testing.T, task *a2a.Task) string {
	t.Helper()
	if len(task.Artifacts) != 1 || len(task.Artifacts[0].Parts) != 1 {
		t.Fatalf("want one artifact with one part, got %+v", task.Artifacts)
	}
	return task.Artifacts[0].Parts[0].Text()
}

// final polls a task until it reaches a terminal state.
func (f *fixture) final(t *testing.T, id a2a.TaskID) *a2a.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := f.agent.GetTask(context.Background(), &a2a.GetTaskRequest{ID: id})
		if err != nil {
			t.Fatalf("cannot get task %s: %v", id, err)
		}
		if task.Status.State.Terminal() {
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s never finished", id)
	return nil
}

// send returns the task a message started.
func (f *fixture) send(t *testing.T, request *a2a.SendMessageRequest) *a2a.Task {
	t.Helper()
	result, err := f.agent.SendMessage(context.Background(), request)
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		t.Fatalf("want a task, got %T", result)
	}
	return task
}

// -- the card and the token --------------------------------------------------

func TestTheCardIsPublicAndNamesTheEndpointAndTheToken(t *testing.T) {
	f := start(t, time.Minute)
	response, err := http.Get(f.endpoint + a2asrv.WellKnownAgentCardPath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the card answered %d", response.StatusCode)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(response.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	if got := card.SupportedInterfaces[0].URL; got != f.endpoint+RPCPath {
		t.Fatalf("the card names %s", got)
	}
	if card.Capabilities.PushNotifications {
		t.Fatal("the card offers push notifications")
	}
	if _, ok := card.SecuritySchemes["bearer"]; !ok || len(card.SecurityRequirements) != 1 {
		t.Fatalf("the card does not require the bearer token: %+v", card.SecurityRequirements)
	}
}

func TestAnRPCWithoutTheTokenIsRefusedAndNothingIsSaid(t *testing.T) {
	f := start(t, time.Minute)
	for _, header := range []string{"", "Bearer wrong", "Bearer " + token + "x", token} {
		request, _ := http.NewRequest("POST", f.endpoint+RPCPath,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{}}`))
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Authorization %q answered %d", header, response.StatusCode)
		}
	}
	if log := f.developer.Log(f.room); len(log) != 0 {
		t.Fatalf("the room heard %+v", log)
	}
}

// -- tasks -------------------------------------------------------------------

func TestTheWorkersNextMessageCompletesTheTask(t *testing.T) {
	f := start(t, time.Minute)
	say(t, f.worker, f.room, "said before the task")

	done := make(chan a2a.SendMessageResult, 1)
	go func() {
		result, _ := f.agent.SendMessage(context.Background(), text("run the tests"))
		done <- result
	}()

	heard(t, f.developer, f.room, "alice", "run the tests")
	// Only the worker answers: the developer speaking is not a reply.
	say(t, f.developer, f.room, "watching")
	say(t, f.worker, f.room, "all 41 pass")

	select {
	case result := <-done:
		task, ok := result.(*a2a.Task)
		if !ok {
			t.Fatalf("want a task, got %T", result)
		}
		if task.Status.State != a2a.TaskStateCompleted {
			t.Fatalf("the task is %s", task.Status.State)
		}
		if got := artifactText(t, task); got != "all 41 pass" {
			t.Fatalf("the artifact is %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the task never completed")
	}
}

func TestOneReplyAnswersEveryTaskHeldDuringTheTurn(t *testing.T) {
	f := start(t, time.Minute)
	first := f.send(t, soon("first"))
	second := f.send(t, soon("second"))
	heard(t, f.developer, f.room, "alice", "first")
	heard(t, f.developer, f.room, "alice", "second")

	say(t, f.worker, f.room, "both done")
	for _, id := range []a2a.TaskID{first.ID, second.ID} {
		if got := artifactText(t, f.final(t, id)); got != "both done" {
			t.Fatalf("task %s got %q", id, got)
		}
	}
}

func TestATaskTheWorkerNeverAnswersFails(t *testing.T) {
	f := start(t, 200*time.Millisecond)
	task := f.send(t, text("are you there"))
	if task.Status.State != a2a.TaskStateFailed {
		t.Fatalf("the task is %s", task.Status.State)
	}
	if got := task.Status.Message.Parts[0].Text(); !strings.Contains(got, "no reply") {
		t.Fatalf("the failure says %q", got)
	}
}

func TestCancelingATaskInterruptsTheWorker(t *testing.T) {
	f := start(t, time.Minute)
	task := f.send(t, soon("long job"))
	heard(t, f.developer, f.room, "alice", "long job")

	canceled, err := f.agent.CancelTask(context.Background(), &a2a.CancelTaskRequest{ID: task.ID})
	if err != nil {
		t.Fatalf("cannot cancel: %v", err)
	}
	if canceled.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("the task is %s", canceled.Status.State)
	}
	heard(t, f.developer, f.room, "alice", "/interrupt")

	// A later reply does not resurrect it.
	say(t, f.worker, f.room, "stopped")
	time.Sleep(100 * time.Millisecond)
	if got := f.final(t, task.ID); got.Status.State != a2a.TaskStateCanceled || len(got.Artifacts) != 0 {
		t.Fatalf("after the reply the task is %s with %+v", got.Status.State, got.Artifacts)
	}
}

func TestAStreamReportsWorkingThenTheArtifactThenCompletion(t *testing.T) {
	f := start(t, time.Minute)
	stream := f.agent.SendStreamingMessage(context.Background(), text("stream it"))

	var states []a2a.TaskState
	var artifact string
	for event, err := range stream {
		if err != nil {
			t.Fatalf("the stream failed: %v", err)
		}
		switch e := event.(type) {
		case *a2a.Task:
			states = append(states, e.Status.State)
		case *a2a.TaskStatusUpdateEvent:
			states = append(states, e.Status.State)
			if e.Status.State == a2a.TaskStateWorking {
				heard(t, f.worker, f.room, "alice", "stream it")
				say(t, f.worker, f.room, "streamed")
			}
		case *a2a.TaskArtifactUpdateEvent:
			artifact = e.Artifact.Parts[0].Text()
		}
	}
	want := []a2a.TaskState{a2a.TaskStateSubmitted, a2a.TaskStateWorking, a2a.TaskStateCompleted}
	if strings.Join(stateNames(states), ",") != strings.Join(stateNames(want), ",") || artifact != "streamed" {
		t.Fatalf("states %v, artifact %q", states, artifact)
	}
}

func stateNames(states []a2a.TaskState) []string {
	names := make([]string, len(states))
	for i, state := range states {
		names[i] = string(state)
	}
	return names
}

// -- refusals ----------------------------------------------------------------

func TestOnlyTextIsTaken(t *testing.T) {
	f := start(t, time.Minute)
	request := &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser,
		a2a.NewTextPart("see attached"), a2a.NewRawPart([]byte("binary")))}
	if _, err := f.agent.SendMessage(context.Background(), request); !errors.Is(err, a2a.ErrUnsupportedContentType) {
		t.Fatalf("a file part answered %v", err)
	}
	if _, err := f.agent.SendMessage(context.Background(), text("   ")); !errors.Is(err, a2a.ErrInvalidParams) {
		t.Fatalf("an empty message answered %v", err)
	}
	if log := f.developer.Log(f.room); len(log) != 0 {
		t.Fatalf("the room heard %+v", log)
	}
}

func TestPushNotificationsAreRefused(t *testing.T) {
	f := start(t, time.Minute)
	task := f.send(t, soon("job"))
	_, err := f.agent.CreateTaskPushConfig(context.Background(),
		&a2a.PushConfig{TaskID: task.ID, URL: "http://127.0.0.1:1/hook"})
	if !errors.Is(err, a2a.ErrPushNotificationNotSupported) {
		t.Fatalf("a push config answered %v", err)
	}
}

func TestTheGatewayCannotFrontItselfOrARoomItCannotRead(t *testing.T) {
	server := testserver.Start(t, 100)
	_, developer, _, err := client.Connect(server.Base, "demo", "demo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(developer.Stop)
	room, err := developer.OpenRoom([]client.Principal{{Kind: "user", ID: "bob"}}, "Run", "persisted")
	if err != nil {
		t.Fatal(err)
	}

	base := Config{Server: server.Base, Password: "alice", Room: room.ID, Token: token}
	self := base
	self.User, self.Worker = "alice", "alice"
	if _, err := Open(self); err == nil || !strings.Contains(err.Error(), "its own") {
		t.Fatalf("fronting itself answered %v", err)
	}
	uninvited := base
	uninvited.User, uninvited.Worker = "alice", "bob"
	if _, err := Open(uninvited); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("an uninvited room answered %v", err)
	}
	tokenless := uninvited
	tokenless.Token = ""
	if _, err := Open(tokenless); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("no token answered %v", err)
	}
}
