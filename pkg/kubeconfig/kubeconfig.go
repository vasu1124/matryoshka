// Copyright 2021 OnMetal authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kubeconfig

import (
	"context"
	"fmt"
	matryoshkav1alpha1 "github.com/onmetal/matryoshka/apis/matryoshka/v1alpha1"
	"github.com/onmetal/matryoshka/pkg/memorystore"
	"github.com/onmetal/matryoshka/pkg/utils"
	"github.com/onmetal/matryoshka/pkg/utils/multigetter"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientcmdapiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Resolver struct {
	scheme *runtime.Scheme
	client client.Client
}

func (r *Resolver) createKubeconfigReferences(ctx context.Context, s *memorystore.Store, kubeconfig *matryoshkav1alpha1.Kubeconfig) error {
	for _, authInfo := range kubeconfig.Spec.AuthInfos {
		if err := r.createAuthInfoReferences(ctx, s, kubeconfig.Namespace, &authInfo.AuthInfo); err != nil {
			return err
		}
	}
	for _, cluster := range kubeconfig.Spec.Clusters {
		if err := r.createClusterReferences(ctx, s, kubeconfig.Namespace, &cluster.Cluster); err != nil {
			return err
		}
	}
	return nil
}

func (r *Resolver) createClusterReferences(ctx context.Context, s *memorystore.Store, namespace string, cluster *matryoshkav1alpha1.Cluster) error {
	if certificateAuthority := cluster.CertificateAuthority; certificateAuthority != nil {
		if err := utils.IgnoreAlreadyExists(s.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      certificateAuthority.Secret.Name,
			},
		})); err != nil {
			return err
		}
	}

	return nil
}

func (r *Resolver) createAuthInfoReferences(ctx context.Context, s *memorystore.Store, namespace string, authInfo *matryoshkav1alpha1.AuthInfo) error {
	if clientCertificate := authInfo.ClientCertificate; clientCertificate != nil {
		if err := utils.IgnoreAlreadyExists(s.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      clientCertificate.Secret.Name,
			},
		})); err != nil {
			return err
		}
	}

	if clientKey := authInfo.ClientKey; clientKey != nil {
		if err := utils.IgnoreAlreadyExists(s.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      clientKey.Secret.Name,
			},
		})); err != nil {
			return err
		}
	}

	if token := authInfo.Token; token != nil {
		if err := utils.IgnoreAlreadyExists(s.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      token.Secret.Name,
			},
		})); err != nil {
			return err
		}
	}

	if password := authInfo.Password; password != nil {
		if err := utils.IgnoreAlreadyExists(s.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      password.Secret.Name,
			},
		})); err != nil {
			return err
		}
	}

	return nil
}

func (r *Resolver) ObjectReferences(ctx context.Context, kubeconfig *matryoshkav1alpha1.Kubeconfig) (*memorystore.Store, error) {
	s := memorystore.New(r.scheme)

	if err := r.createKubeconfigReferences(ctx, s, kubeconfig); err != nil {
		return nil, err
	}

	return s, nil
}

func (r *Resolver) resolveKubeconfigObjects(ctx context.Context, s *memorystore.Store) error {
	mg, err := multigetter.New(multigetter.Options{Client: r.client})
	if err != nil {
		return err
	}

	if err := mg.MultiGet(ctx, multigetter.RequestsFromObjects(s.Objects())...); err != nil {
		return err
	}

	return nil
}

func (r *Resolver) Resolve(ctx context.Context, kubeconfig *matryoshkav1alpha1.Kubeconfig) (*clientcmdapiv1.Config, error) {
	s, err := r.ObjectReferences(ctx, kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("error determining objects referenced by kubeconfig: %w", err)
	}

	if err := r.resolveKubeconfigObjects(ctx, s); err != nil {
		return nil, fmt.Errorf("error resolving objects referenced by kubeconfig: %w", err)
	}

	cfg, err := r.resolveKubeconfig(ctx, s, kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("error resolving kubeconfig to config: %w", err)
	}

	return cfg, nil
}

func (r *Resolver) resolveKubeconfig(ctx context.Context, s *memorystore.Store, kubeconfig *matryoshkav1alpha1.Kubeconfig) (*clientcmdapiv1.Config, error) {
	authInfos := make([]clientcmdapiv1.NamedAuthInfo, 0, len(kubeconfig.Spec.AuthInfos))
	for _, authInfo := range kubeconfig.Spec.AuthInfos {
		resolved, err := r.resolveAuthInfo(ctx, s, kubeconfig.Namespace, &authInfo.AuthInfo)
		if err != nil {
			return nil, err
		}

		authInfos = append(authInfos, clientcmdapiv1.NamedAuthInfo{Name: authInfo.Name, AuthInfo: *resolved})
	}

	clusters := make([]clientcmdapiv1.NamedCluster, 0, len(kubeconfig.Spec.Clusters))
	for _, cluster := range kubeconfig.Spec.Clusters {
		resolved, err := r.resolveCluster(ctx, s, kubeconfig.Namespace, &cluster.Cluster)
		if err != nil {
			return nil, err
		}

		clusters = append(clusters, clientcmdapiv1.NamedCluster{Name: cluster.Name, Cluster: *resolved})
	}

	contexts := make([]clientcmdapiv1.NamedContext, 0, len(kubeconfig.Spec.Contexts))
	for _, context := range kubeconfig.Spec.Contexts {
		contexts = append(contexts, clientcmdapiv1.NamedContext{
			Name: context.Name,
			Context: clientcmdapiv1.Context{
				Cluster:   context.Context.Cluster,
				AuthInfo:  context.Context.AuthInfo,
				Namespace: context.Context.Namespace,
			},
		})
	}

	return &clientcmdapiv1.Config{
		Clusters:       clusters,
		AuthInfos:      authInfos,
		Contexts:       contexts,
		CurrentContext: kubeconfig.Spec.CurrentContext,
	}, nil
}

func (r *Resolver) resolveAuthInfo(
	ctx context.Context,
	s *memorystore.Store,
	namespace string,
	authInfo *matryoshkav1alpha1.AuthInfo,
) (*clientcmdapiv1.AuthInfo, error) {
	var clientCertificateData []byte
	if clientCertificate := authInfo.ClientCertificate; clientCertificate != nil {
		var err error
		clientCertificateData, err = utils.GetSecretSelector(ctx, s, namespace, *clientCertificate.Secret, matryoshkav1alpha1.DefaultAuthInfoClientCertificateKey)
		if err != nil {
			return nil, err
		}
	}

	var clientKeyData []byte
	if clientKey := authInfo.ClientKey; clientKey != nil {
		var err error
		clientKeyData, err = utils.GetSecretSelector(ctx, s, namespace, *clientKey.Secret, matryoshkav1alpha1.DefaultAuthInfoClientKeyKey)
		if err != nil {
			return nil, err
		}
	}

	var token string
	if tok := authInfo.Token; tok != nil {
		var (
			tokenData []byte
			err       error
		)
		tokenData, err = utils.GetSecretSelector(ctx, s, namespace, *tok.Secret, matryoshkav1alpha1.DefaultAuthInfoTokenKey)
		if err != nil {
			return nil, err
		}

		token = string(tokenData)
	}

	var password string
	if pwd := authInfo.Password; pwd != nil {
		var (
			passwordData []byte
			err          error
		)
		passwordData, err = utils.GetSecretSelector(ctx, s, namespace, *pwd.Secret, matryoshkav1alpha1.DefaultAuthInfoPasswordKey)
		if err != nil {
			return nil, err
		}

		password = string(passwordData)
	}

	return &clientcmdapiv1.AuthInfo{
		ClientCertificateData: clientCertificateData,
		ClientKeyData:         clientKeyData,
		Token:                 token,
		Impersonate:           authInfo.Impersonate,
		ImpersonateGroups:     authInfo.ImpersonateGroups,
		Username:              authInfo.Username,
		Password:              password,
	}, nil
}

func (r *Resolver) resolveCluster(ctx context.Context, s *memorystore.Store, namespace string, cluster *matryoshkav1alpha1.Cluster) (*clientcmdapiv1.Cluster, error) {
	var certificateAuthorityData []byte
	if certificateAuthority := cluster.CertificateAuthority; certificateAuthority != nil {
		var err error
		certificateAuthorityData, err = utils.GetSecretSelector(ctx, s, namespace, *certificateAuthority.Secret, matryoshkav1alpha1.DefaultClusterCertificateAuthorityKey)
		if err != nil {
			return nil, err
		}
	}

	return &clientcmdapiv1.Cluster{
		Server:                   cluster.Server,
		TLSServerName:            cluster.TLSServerName,
		InsecureSkipTLSVerify:    cluster.InsecureSkipTLSVerify,
		CertificateAuthorityData: certificateAuthorityData,
		ProxyURL:                 cluster.ProxyURL,
	}, nil
}

type ResolverOptions struct {
	Client client.Client
	Scheme *runtime.Scheme
}

func (o *ResolverOptions) Validate() error {
	if o.Client == nil {
		return fmt.Errorf("client needs to be set")
	}
	if o.Scheme == nil {
		return fmt.Errorf("scheme needs to be set")
	}
	return nil
}

func NewResolver(opts ResolverOptions) (*Resolver, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	return &Resolver{
		scheme: opts.Scheme,
		client: opts.Client,
	}, nil
}
