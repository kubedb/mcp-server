package k8s

// API groups in the KubeDB family.
const (
	GroupKubeDB      = "kubedb.com"
	GroupOps         = "ops.kubedb.com"
	GroupCatalog     = "catalog.kubedb.com"
	GroupAutoscaling = "autoscaling.kubedb.com"
	GroupSchema      = "schema.kubedb.com"
	GroupArchiver    = "archiver.kubedb.com"
	GroupKafka       = "kafka.kubedb.com"
	GroupPostgres    = "postgres.kubedb.com"
	GroupUI          = "ui.kubedb.com"
	GroupGitOps      = "gitops.kubedb.com"
	GroupElastic     = "elasticsearch.kubedb.com"
)

// OpsRequestTypes lists the operation types shared across database kinds.
// Individual databases support a subset, plus a few database specific types
// such as ForceFailOver (Postgres) or ReplaceSentinel (Redis).
var OpsRequestTypes = []string{
	"UpdateVersion",
	"HorizontalScaling",
	"VerticalScaling",
	"VolumeExpansion",
	"Restart",
	"Reconfigure",
	"ReconfigureTLS",
	"RotateAuth",
	"StorageMigration",
	"Reprovision",
}

// OpsSpecKey maps an ops request type to the spec field that carries its
// payload in the OpsRequest object.
var OpsSpecKey = map[string]string{
	"UpdateVersion":     "updateVersion",
	"HorizontalScaling": "horizontalScaling",
	"VerticalScaling":   "verticalScaling",
	"VolumeExpansion":   "volumeExpansion",
	"Reconfigure":       "configuration",
	"ReconfigureTLS":    "tls",
	"RotateAuth":        "authentication",
	"Restart":           "restart",
	"StorageMigration":  "migration",
	"Reprovision":       "reprovision",
}

// DeletionPolicies are the valid spec.deletionPolicy values.
var DeletionPolicies = []string{"Delete", "WipeOut", "Halt", "DoNotTerminate"}

// OpsKindFor returns the OpsRequest kind for a database kind.
func OpsKindFor(dbKind string) string { return dbKind + "OpsRequest" }

// AutoscalerKindFor returns the Autoscaler kind for a database kind.
func AutoscalerKindFor(dbKind string) string { return dbKind + "Autoscaler" }

// VersionKindFor returns the catalog Version kind for a database kind.
func VersionKindFor(dbKind string) string { return dbKind + "Version" }
