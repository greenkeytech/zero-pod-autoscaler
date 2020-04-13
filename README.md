# Zero Pod Autoscaler

Zero Pod Autoscaler (ZPA) manages a Deployment resource. It can scale
the Deployment all the way down to zero replicas when it is not in
use. It can work alongside an HPA: when scaled to zero, the HPA
ignores the Deployment; once scaled back to one, the HPA may scale up
further.

To do this, ZPA is a **TCP proxy** in front of the Service load balancing
a Deployment. This allows the ZPA to track the number of connections
and scale the Deployment to zero when the connection count has been
zero for some time period.

If scaled to zero, an incoming connection triggers a scale-to-one
action. Once Service's Endpoints include a "ready" addresss, the
connection can complete, hopefully before the client times out.
