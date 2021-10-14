---
authors: Joel Wejdenstål (jwejdenstal@goteleport.com)
state: draft
---

# RFD 43 - Configurable Multiparty Sessions for Kubernetes Access

## What

Implement multiparty sessions for Kubernetes Access using a more advanced model
with support for configurable conditions similar to those of [RFD 26](https://github.com/gravitational/teleport/blob/2fd6a88800604342bfa6277060b056d8bf0cbfb2/rfd/0026-custom-approval-conditions.md).
Also support defining conditions for required observrs in order to initiate and maintain a session.

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
- Alice see:
```
Creating session with uuid <example-uuid>...
Session pending: observer requirements not met
- role: auditor-role x2
```
- Eve joins the session with `tsh kube join <example-uuid>` and sees:
```
Please tap MFA key to continue...
```
- Eve taps MFA
- Alice and Eve see:
```
Creating session with uuid <example-uuid>...
Session pending: observer requirements not met
- role: auditor-role x1
Events:
- User Eve joined the session.
```
- Ben joins the session with `tsh kube join <example-uuid>` and sees:
```
Please tap MFA key to continue...
```
- Ben taps MFA
- Alice, Eve and Ben see
```
Creating session with uuid <example-uuid>...
Session starting...
Events:
- User Eve joined the session.
- User Ben joined the session
```
- The connection to the pod is made and each the session turns into a normal shell.

### Configurable Model Proposition

Instead of having fixed fields for specifying values such as required session viewers and roles this
model centers around conditional allow rules.

Imagine that you want to require two users with the role `auditor` to be present in a session
at all times for pods labeled `environment:prod` or alternatively two viewers with the role `admin`.
A tertiary policy allows the session to start with one `admin` and one `auditor` viewer.

Then a role could look like this:

```yaml
kind: role
version: v4
metadata:
  name: developer
spec:
  allow:
    kubernetes_groups: ["system:masters"]
    kubernetes_pods:
      - name: Auditor Policy
        pod_labels: ["environment:prod"]
        filter: 'contains(viewer.roles, "auditor") || contains(viewer.traits["teams"], "auditors")'
        viewers: 2
      - name: Admin Policy
          pod_labels: ["environment:prod"]
          filter: 'contains(viewer.roles, "admin") || contains(viewer.traits["teams"], "admins")'
          viewers: 2
      - name: Admin and Auditor Policy
          pod_labels: ["environment:prod"]
          filters:
            auditor:
              filter: 'contains(viewer.roles, "auditor") || contains(viewer.traits["teams"], "auditors")'
              viewers: 1
            admin:
              filter: 'contains(viewer.roles, "admin") || contains(viewer.traits["teams"], "admins")'
              viewers: 1
```

What this system attempts to achieve is to allow administrators to construct rich
matchers for who can serve as a required viewer for whom for some set of pods.

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
