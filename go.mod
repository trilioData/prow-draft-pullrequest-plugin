module draft-plugin

go 1.16

replace k8s.io/client-go => k8s.io/client-go v0.22.2

require (
	cloud.google.com/go/container v1.0.0 // indirect
	cloud.google.com/go/monitoring v1.0.0 // indirect
	cloud.google.com/go/trace v1.0.0 // indirect
	github.com/gomodule/redigo v2.0.0+incompatible // indirect
	github.com/googleapis/gax-go/v2 v2.1.1 // indirect
	github.com/sirupsen/logrus v1.8.1
	golang.org/x/net v0.0.0-20210917221730-978cfadd31cf // indirect
	golang.org/x/sys v0.1.0 // indirect
	golang.org/x/text v0.3.7 // indirect
	k8s.io/apimachinery v0.22.2
	k8s.io/client-go v11.0.1-0.20190805182717-6502b5e7b1b5+incompatible
	k8s.io/test-infra v0.0.0-20211019234757-1ed77f84f5db
	sigs.k8s.io/controller-runtime v0.9.0
)
