---
authors: Joel Wejdenstål (jwejdenstal@goteleport.com)
state: draft
---

# RFD 43 - Shared sessions with observers for Kubernetes Access

## What

Implement joint observer support for Kubernetes Access with support for configurable conditions similar to those of [RFD 26](https://github.com/gravitational/teleport/blob/2fd6a88800604342bfa6277060b056d8bf0cbfb2/rfd/0026-custom-approval-conditions.md).
Also support defining conditions for required observers in order to initiate and maintain a session.

## Why

Heavily regulated and security critical industries require that one or more observers with a certain role
are present in Kubernetes Access sessions and viewing it live in order to guarantee that
operator does not perform any mistakes or acts of malice.

Such observers need to have the power to end or lock a session immediately should anything go wrong.

To suit everyone this will need a more detailed configuration model based on rules
that can be used to define observers, their powers and when and in what capacity they are required.

## Details

### Multiparty sessions

SSH sessions via TSH currently have rich support for sessions with multiple users at once.
This concept is to be extended to Kubernetes Access which will allow us to build additional features on top.

Multiparty sessions shall be implemented by modifying the k8s request proxy forwarder in the `kubernetes_service`. This
approach was chosen as it is a hub that sessions pass through which makes it optimal for multiplexing.

An approach using multiplexing in the `proxy_service` layer was considered but was ultimately determined to be more complicated
due to the fact that proxies don't handle the final session traffic hop when using Kubernetes Access.

It will work by adding a multiplexing layer inside the forwarder that similar to the current session recording
functionality, but instead this multiplexes outputs to the session initiator and all observers
but only streams back input from the initiator.

#### Session observers

A core feature we need to support is required observers. This will allow cluster administrators to configure
policies that require certain Teleport users of a certain role to be actively monitoring the session. Each of these
observers will also have the power to lock input/output for the session controller or instantly end it.

This feature is useful in security critical environments where multiple people need to witness every action
in the event of severe error or malice and have the ability to halt any erroneous or malicious action.

#### Session states

By default, a `kubectl exec` and `kubectl attach` request will go through if no policies are defined.
If a policy like the one above is defined the session will be put in a pending state
until the required viewers have joined.

Sessions can have 3 possible states:

- `PENDING`\
  When a session is in a `PENDING` state, the connection to the pod from the proxy has not yet started
  and all users are shown a default message informing them that the session is pending, current participants
  and who else is required for the session to start.
- `RUNNING`\
A `RUNNING` session behaves like a normal multiparty session. `stdout`, `stdin` and `stdout` are mapped as usual
  and the pod can be interacted with.
- `TERMINATED`\
  A session becomes `TERMINATED` once the shell spawned inside the pod quits or is forcefully terminated by one of the session participants.

All sessions begin in the `PENDING` state and can change states based on the following transitions:

##### Transition 1: `PENDING -> RUNNING`

When the requirements for present viewers laid out in the role policy are fulfilled,
the session transitions to a `RUNNING` state. This involves initiating the connection to the pod
and setting up the shell. Finally, all clients are multiplexed
onto the correct streams as described previously.

Only the session initiator is able to make input, observers are not connected to the input stream
and may only view stdout/stderr and terminate the session.

##### Transition 2: `RUNNING -> TERMINATED`

When the shell process created on the pod is terminated, the session transitions to a `TERMINATED` state and all clients
are disconnected as per standard `kubectl` behaviour.

##### Transition 3: `RUNNING -> TERMINATED`

Session observers that are present may at any point decide to forcefully terminate the session.
This will instantly disconnect input and output streams to prevent further communication. Once this is done
the Kubernetes proxy will send a termination request to the pod session to request it be stopped.

##### Transition 4: `RUNNING -> PENDING`

If an observer disconnects from the session in a way that causes the policy for required observers to suddenly not be fulfilled,
the session will transition back to a `PENDING` state. In this state, input and output streams are disconnected, preventing any further action.

Here, the connection is frozen for a configurable amount of time as a sort of grace period.

##### Transition 5: `PENDING -> TERMINATED`

After a grace period has elapsed in a session in a session that previously was in a `RUNNING`
state, the session is automatically terminated. This can be cancelled by having the required observers
join back in which transitions the session back to `RUNNING`.

##### Transition 6: `PENDING -> TERMINATED`

Any participant of the session can terminate the session in the `PENDING` state.
This will simply mark the session as terminated and disconnect the participants as no
connection to the pod exists at this time.

#### UI/UX

The initial implementation of multiparty sessions on Kubernetes access will only be supported via CLI access for implementation simplicity.

Terminating the `kubectl` process that started the session terminates the session. Terminating an observer `tsh` process
disconnects the observer from the session and applies relevant state transitions if any.

Terminating the session from a observer `tsh` instance can be done with the key combination ´CTRL-T`

##### Session creation

Session creation UX is unmodified and happens as usual via `kubectl exec`.
A session UUID is displayed when executing `kubectl exec` which allows others to connect.

##### Session join

`kubectl` itself has no concept of multiparty sessions. This means that we cannot easily use
its built-in facilities for support session joining.

To make this process easier for the user. I propose extending the current `tsh join` command
to also work for Kubernetes access in the form of `tsh kube join <session-id>`. This attaches
to an ongoing session and displays stdout/stderr.

##### Example

This example illustrates how a group 3 users of which Alice is the initiator and Eve and Ben are two observers
start a multiparty session. Below is a a series of events that happen that include what each user sees and what
they do.

- Alice initiates an interactive session to a pod: `kubectl exec -it database -- sh`
- Alice sees:
```
Creating session with uuid <example-uuid>...
This session requires moderator. Waiting for others to join:
- role: auditor-role x2
```
- Eve joins the session with `tsh kube join <example-uuid>` and sees:
```
Please tap MFA key to continue...
```
- Eve taps MFA
- Alice and Eve sees:
```
Creating session with uuid <example-uuid>...
This session requires moderator. Waiting for others to join:
- role: auditor-role x1
Events:
- User Eve joined the session.
```
- Ben joins the session with `tsh kube join <example-uuid>` and sees:
```
Please tap MFA key to continue...
```
- Ben taps MFA
- Alice, Eve and Ben sees
```
Creating session with uuid <example-uuid>...
Session starting...
Events:
- User Eve joined the session.
- User Ben joined the session
```
- The connection to the pod is made and each the session turns into a normal shell.

#### Participant requests

Shared sessions for Kubernetes access will have support for participant requests.
A participant request may be created in the `PENDING` session state by a session participant interactively.

This creates a resource that can be seen by eligible session participants with `tsh kube requests ls`.
This easily allows eligible participants to find and join a session waiting for participants.

A request may also be created for an existing session via `tsh kube requests create <session-id>`.
It will have an optional flag for suggested participants: `tsh kube requests create <session-id> --invite user1,user2,user3`.

The request is associated with the same ID as that of the session which makes these notifications
resources work seamlessly with `tsh kube join`

An optional reason flag also exists which allows you to attach an arbitrary message to the participant request: `tsh kube requests create <session-id> --reason "customer db maintenance"`.

A resource will be created which will support interaction with existing plugins for notifiying relevant
groups when a request is created similar to notifications for access requests.

##### Request resource

The resource has been modeled after the resource for access requests. Below follows the protobuf
declaration of the proposed resource.

```protobuf
// KubernetesSessionRequestSpecV3 is the specification for a request for session participants resource.
message KubernetesSessionRequestSpecV3 {
    // SessionID is unique identifier of the session after 
    string SessionID = 1 [ (gogoproto.jsontag) = "user" ];

    // State is the current state of this session request.
    KubernetesSessionRequestState State = 3 [ (gogoproto.jsontag) = "state,omitempty" ];

    // Created encodes the time at which the request was registered with the auth
    // server.
    google.protobuf.Timestamp Created = 4 [
        (gogoproto.stdtime) = true,
        (gogoproto.nullable) = false,
        (gogoproto.jsontag) = "created,omitempty"
    ];

    // Expires encodes the time at which this session participant request expires and becomes invalid.
    google.protobuf.Timestamp Expires = 4 [
        (gogoproto.stdtime) = true,
        (gogoproto.nullable) = false,
        (gogoproto.jsontag) = "expires,omitempty"
    ];

    // RequestReason is an optional message explaining the reason for the request.
    string RequestReason = 6 [ (gogoproto.jsontag) = "request_reason,omitempty" ];

    // SuggestedReviewers is a list of reviewer suggestions.  These can be teleport usernames, but
    // that is not a requirement.
    repeated string SuggestedReviewers = 13
        [ (gogoproto.jsontag) = "suggested_reviewers,omitempty" ];
}

// KubernetesSessionRequestState represents the state of a request for escalated privilege.
enum KubernetesSessionRequestState {
    // PENDING variant represents a session that is waiting on participants to fulfill the criteria
    // to start the session.
    PENDING = 0;

    // FULFILLED variant represents a session that has had it's criteria for starting
    // fulfilled at least once and has transitioned to a RUNNING state.
    FULFILLED = 1;
}
```

### Configurable Model Proposition

Instead of having fixed fields for specifying values such as required session viewers and roles this
model centers around conditional allow rules and filters. It is implemented as a bi-directional mapping between the role of the session initiator and the roles of the other session participants.

Roles can have `require_session_join` rule under `allow` containing requirements for session participants
before a session may be started with privilege access to nodes that the role provides.

Roles can also have an `join_sessions` rule under `allow` specifying which roles
and session types that that the role grants privileges to join.

We will only initially support the modes `moderator` for Kubernetes Access and `peer` for SSH sessions.
An `observer` mode also exists which only grants access to view but not terminate an ongoing session.

Imagine you have 4 roles:
- `prod-access`
- `senior-dev`
- `customer-db-maintenance`
- `maintenance-observer`

And these requirements:
- `prod-access` should be able to start sessions of any type with either one `senior-dev` observeror two `dev` observers.
- `senior-dev` should be able to start sessions of any type without oversight.
- `customer-db-maintenance` needs oversight by one `maintenance-observer` on `ssh` type sessions.

Then the 4 roles could be defined as follows:

```yaml
kind: role
metadata:
  name: prod-access
spec:
  allow:
    require_session_join:
      - name: Senior dev oversight
        filter: 'contains(observer.roles,"senior-dev")'
        kinds: ['k8s', 'ssh']
        modes: ['moderator']
        count: 1
      - name: Dual dev oversight
        filter: 'contains(observer.roles,"dev")'
        kinds: ['k8s', 'ssh']
        modes: ['moderator']
        count: 2
```

```yaml
kind: role
metadata:
  name: senior-dev
spec:
  allow:
    join_sessions:
      - name: Senior dev oversight
        roles : ['prod-access', 'training']
        kinds: ['k8s', 'ssh', 'db']
        modes: ['moderator']
```

```yaml
kind: role
metadata:
  name: customer-db-maintenance
spec:
  allow:
    require_session_join:
      - name: Maintenance oversight
        filter: 'contains(observer.roles, "maintenance-observer")'
        kinds: ['ssh']
        modes: ['moderator']
        count: 1
```

```yaml
kind: role
metadata:
  name: maintenance-observer
spec:
  allow:
    join_sessions:
      - name: Maintenance oversight
        roles: ['customer-db-*']
        kind: ['*']
        modes: ['moderator']
```

#### Filter specification

A filter determines if a user may act as an approved observer or not.
To facilitate more complex configurations which may be desired we borrow some ideas from the `where` clause used by resource rules.

To make it more workable, the language has been slimmed down significantly to handle this particular use case very well.

##### Functions

- `set["key"]`: Set and array indexing
- `contains(set, item)`: Determines if the set contains the item or not.

##### Provided variables

- `viewer`
```json
{
  "traits": "map<string, []string>",
  "roles": "[]string",
  "name": "string"
}
```

##### Grammar

The grammar and other language is otherwise equal to that of the `where` clauses used by resource rules and the language
used by approval requests, This promotes consistency across the product, reducing confusion.
