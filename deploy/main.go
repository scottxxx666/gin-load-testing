package main

import (
	"fmt"
	"github.com/pulumi/pulumi-gcp/sdk/v3/go/gcp/container"
	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v2/go/kubernetes"
	appsv1 "github.com/pulumi/pulumi-kubernetes/sdk/v2/go/kubernetes/apps/v1"
	autoscalev2 "github.com/pulumi/pulumi-kubernetes/sdk/v2/go/kubernetes/autoscaling/v2beta2"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v2/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v2/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v2/go/pulumi"
	"strings"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) (err error) {
		cluster, err := container.NewCluster(ctx, "load-testing", &container.ClusterArgs{
			InitialNodeCount:      pulumi.Int(1),
			RemoveDefaultNodePool: pulumi.Bool(true),
			Location:              pulumi.String("asia-east1-b"),
			ClusterAutoscaling: container.ClusterClusterAutoscalingArgs{
				Enabled:            pulumi.Bool(true),
				AutoscalingProfile: pulumi.String("OPTIMIZE_UTILIZATION"),
				ResourceLimits: container.ClusterClusterAutoscalingResourceLimitArray{
					container.ClusterClusterAutoscalingResourceLimitArgs{
						ResourceType: pulumi.String("cpu"),
						Minimum:      pulumi.Int(1),
						Maximum:      pulumi.Int(4),
					},
					container.ClusterClusterAutoscalingResourceLimitArgs{
						ResourceType: pulumi.String("memory"),
						Minimum:      pulumi.Int(1),
						Maximum:      pulumi.Int(40),
					},
				},
			},
		})
		if err != nil {
			return err
		}

		pool, err := container.NewNodePool(ctx, "primary-node-pool", &container.NodePoolArgs{
			Cluster:          cluster.Name,
			InitialNodeCount: pulumi.Int(1),
			Location:         pulumi.String("asia-east1-b"),
			Autoscaling: container.NodePoolAutoscalingArgs{
				MaxNodeCount: pulumi.Int(4),
				MinNodeCount: pulumi.Int(1),
			},
			NodeConfig: &container.NodePoolNodeConfigArgs{
				Labels: pulumi.StringMap{
					"env": pulumi.String("test"),
				},
				Metadata: pulumi.StringMap{
					"disable-legacy-endpoints": pulumi.String("true"),
				},
				OauthScopes: pulumi.StringArray{
					pulumi.String("https://www.googleapis.com/auth/logging.write"),
					pulumi.String("https://www.googleapis.com/auth/monitoring"),
				},
				Tags: pulumi.StringArray{
					pulumi.String("foo"),
					pulumi.String("bar"),
				},
			},
		})
		if err != nil {
			return err
		}

		k8sProvider, err := k8s.NewProvider(ctx, "k8sprovider", &k8s.ProviderArgs{
			Kubeconfig: genKubeconfig(cluster.Endpoint, cluster.Name, cluster.MasterAuth),
		}, pulumi.DependsOn([]pulumi.Resource{pool}))
		if err != nil {
			return err
		}

		namespace, err := corev1.NewNamespace(ctx, "app-ns", &corev1.NamespaceArgs{
			Metadata: metav1.ObjectMetaArgs{
				Name: pulumi.String("load-testing-ns"),
			},
		}, pulumi.Provider(k8sProvider))
		if err != nil {
			return err
		}

		appLabels := pulumi.StringMap{"app": pulumi.String("load-testing")}

		deployment, err := appsv1.NewDeployment(ctx, "load-testing-app", &appsv1.DeploymentArgs{
			Metadata: metav1.ObjectMetaArgs{
				Namespace: namespace.Metadata.Elem().Name(),
			},
			Spec: appsv1.DeploymentSpecArgs{
				Selector: &metav1.LabelSelectorArgs{
					MatchLabels: appLabels,
				},
				Replicas: pulumi.Int(1),
				Template: &corev1.PodTemplateSpecArgs{
					Metadata: metav1.ObjectMetaArgs{
						Labels: appLabels,
					},
					Spec: &corev1.PodSpecArgs{
						Containers: corev1.ContainerArray{
							corev1.ContainerArgs{
								Name:  pulumi.String("load-testing-dep"),
								Image: pulumi.String("scottxxx666/gin-load-testing:0.0.1"),
								Resources: corev1.ResourceRequirementsArgs{
									Requests: pulumi.StringMap{"cpu": pulumi.String("100m")},
								},
							},
						},
					},
				},
			},
		}, pulumi.Provider(k8sProvider))
		if err != nil {
			return err
		}

		depName := deployment.ID().ApplyString(func(id interface{}) (string, error) {
			s := strings.Split(fmt.Sprintf("%s", id), "/")
			return s[len(s)-1], nil
		})
		if err != nil {
			return err
		}

		_, err = autoscalev2.NewHorizontalPodAutoscaler(ctx, "load-testing-hpa", &autoscalev2.HorizontalPodAutoscalerArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: namespace.Metadata.Elem().Name(),
				Labels:    appLabels,
			},
			Spec: autoscalev2.HorizontalPodAutoscalerSpecArgs{
				MaxReplicas: pulumi.Int(50),
				ScaleTargetRef: autoscalev2.CrossVersionObjectReferenceArgs{
					ApiVersion: pulumi.String("apps/v1"),
					Kind:       pulumi.String("Deployment"),
					Name:       depName,
				},
				MinReplicas: pulumi.Int(1),
				Metrics: autoscalev2.MetricSpecArray{
					autoscalev2.MetricSpecArgs{
						Type: pulumi.String("Resource"),
						Resource: autoscalev2.ResourceMetricSourceArgs{
							Name: pulumi.String("cpu"),
							Target: autoscalev2.MetricTargetArgs{
								Type:               pulumi.String("Utilization"),
								AverageUtilization: pulumi.Int(50),
							},
						},
					},
				},
			},
		}, pulumi.Provider(k8sProvider))
		if err != nil {
			return err
		}

		service, err := corev1.NewService(ctx, "app-service", &corev1.ServiceArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Namespace: namespace.Metadata.Elem().Name(),
				Labels:    appLabels,
			},
			Spec: &corev1.ServiceSpecArgs{
				Ports: corev1.ServicePortArray{
					corev1.ServicePortArgs{
						Port:       pulumi.Int(80),
						TargetPort: pulumi.Int(8080),
					},
				},
				Selector: appLabels,
				Type:     pulumi.String("LoadBalancer"),
			},
		}, pulumi.Provider(k8sProvider))
		if err != nil {
			return err
		}

		ctx.Export("url", service.Status.ApplyT(func(status *corev1.ServiceStatus) *string {
			ingress := status.LoadBalancer.Ingress[0]
			if ingress.Hostname != nil {
				return ingress.Hostname
			}
			return ingress.Ip
		}))

		return nil
	})

}

func genKubeconfig(clusterEndpoint, clusterName pulumi.StringOutput, clusterMasterAuth container.ClusterMasterAuthOutput) pulumi.StringOutput {
	context := pulumi.Sprintf("demo_%s", clusterName)

	return pulumi.Sprintf(`apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: %s
    server: https://%s
  name: %s
contexts:
- context:
    cluster: %s
    user: %s
  name: %s
current-context: %s
kind: Config
preferences: {}
users:
- name: %s
  user:
    auth-provider:
      config:
        cmd-args: config config-helper --format=json
        cmd-path: gcloud
        expiry-key: '{.credential.token_expiry}'
        token-key: '{.credential.access_token}'
      name: gcp`,
		clusterMasterAuth.ClusterCaCertificate().Elem(),
		clusterEndpoint, context, context, context, context, context, context)
}
