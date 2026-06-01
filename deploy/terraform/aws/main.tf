provider "aws" {
  region = var.region
}

# ---------------------------------------------------------------------------
# Data: caller identity + current region
# ---------------------------------------------------------------------------
data "aws_caller_identity" "current" {}

data "aws_availability_zones" "available" {
  state = "available"
}

# ---------------------------------------------------------------------------
# VPC
# ---------------------------------------------------------------------------
resource "aws_vpc" "ubag" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_hostnames = true
  enable_dns_support   = true

  tags = {
    Name                                        = "${var.cluster_name}-vpc"
    "kubernetes.io/cluster/${var.cluster_name}" = "shared"
  }
}

resource "aws_internet_gateway" "ubag" {
  vpc_id = aws_vpc.ubag.id

  tags = { Name = "${var.cluster_name}-igw" }
}

resource "aws_subnet" "public" {
  count = 3

  vpc_id                  = aws_vpc.ubag.id
  cidr_block              = cidrsubnet("10.0.0.0/16", 8, count.index)
  availability_zone       = data.aws_availability_zones.available.names[count.index]
  map_public_ip_on_launch = true

  tags = {
    Name                                        = "${var.cluster_name}-pub-${count.index}"
    "kubernetes.io/cluster/${var.cluster_name}" = "shared"
    "kubernetes.io/role/elb"                    = "1"
  }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.ubag.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.ubag.id
  }

  tags = { Name = "${var.cluster_name}-rt" }
}

resource "aws_route_table_association" "public" {
  count          = length(aws_subnet.public)
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# ---------------------------------------------------------------------------
# IAM: EKS cluster role
# ---------------------------------------------------------------------------
data "aws_iam_policy_document" "eks_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "eks_cluster" {
  name               = "${var.cluster_name}-cluster-role"
  assume_role_policy = data.aws_iam_policy_document.eks_assume.json
}

resource "aws_iam_role_policy_attachment" "eks_cluster_policy" {
  role       = aws_iam_role.eks_cluster.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
}

# ---------------------------------------------------------------------------
# IAM: EKS node group role
# ---------------------------------------------------------------------------
data "aws_iam_policy_document" "node_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "eks_node" {
  name               = "${var.cluster_name}-node-role"
  assume_role_policy = data.aws_iam_policy_document.node_assume.json
}

resource "aws_iam_role_policy_attachment" "eks_worker_node" {
  role       = aws_iam_role.eks_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy"
}

resource "aws_iam_role_policy_attachment" "eks_cni" {
  role       = aws_iam_role.eks_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy"
}

resource "aws_iam_role_policy_attachment" "eks_ecr_read" {
  role       = aws_iam_role.eks_node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly"
}

# ---------------------------------------------------------------------------
# EKS cluster
# ---------------------------------------------------------------------------
resource "aws_eks_cluster" "ubag" {
  name     = var.cluster_name
  version  = var.kubernetes_version
  role_arn = aws_iam_role.eks_cluster.arn

  vpc_config {
    subnet_ids = aws_subnet.public[*].id
  }

  depends_on = [aws_iam_role_policy_attachment.eks_cluster_policy]
}

# ---------------------------------------------------------------------------
# EKS managed node group
# ---------------------------------------------------------------------------
resource "aws_eks_node_group" "ubag" {
  cluster_name    = aws_eks_cluster.ubag.name
  node_group_name = "${var.cluster_name}-workers"
  node_role_arn   = aws_iam_role.eks_node.arn
  subnet_ids      = aws_subnet.public[*].id
  instance_types  = [var.node_size]

  scaling_config {
    desired_size = var.node_count
    min_size     = 1
    max_size     = var.node_count * 2
  }

  update_config {
    max_unavailable = 1
  }

  depends_on = [
    aws_iam_role_policy_attachment.eks_worker_node,
    aws_iam_role_policy_attachment.eks_cni,
    aws_iam_role_policy_attachment.eks_ecr_read,
  ]
}

# ---------------------------------------------------------------------------
# kubeconfig file (written locally for Helm provider)
# ---------------------------------------------------------------------------
locals {
  kubeconfig_content = yamlencode({
    apiVersion = "v1"
    kind       = "Config"
    clusters = [{
      name = aws_eks_cluster.ubag.name
      cluster = {
        server                     = aws_eks_cluster.ubag.endpoint
        certificate-authority-data = aws_eks_cluster.ubag.certificate_authority[0].data
      }
    }]
    users = [{
      name = "eks-user"
      user = {
        exec = {
          apiVersion = "client.authentication.k8s.io/v1beta1"
          command    = "aws"
          args       = ["eks", "get-token", "--cluster-name", aws_eks_cluster.ubag.name, "--region", var.region]
        }
      }
    }]
    contexts = [{
      name = aws_eks_cluster.ubag.name
      context = {
        cluster = aws_eks_cluster.ubag.name
        user    = "eks-user"
      }
    }]
    current-context = aws_eks_cluster.ubag.name
  })

  kubeconfig_path = "${path.module}/.kubeconfig"
}

resource "local_file" "kubeconfig" {
  content         = local.kubeconfig_content
  filename        = local.kubeconfig_path
  file_permission = "0600"

  depends_on = [aws_eks_node_group.ubag]
}

# ---------------------------------------------------------------------------
# Optional: RDS PostgreSQL
# ---------------------------------------------------------------------------
resource "aws_db_subnet_group" "ubag" {
  count = var.enable_postgres ? 1 : 0

  name       = "${var.cluster_name}-db-subnet"
  subnet_ids = aws_subnet.public[*].id
}

resource "aws_db_instance" "ubag" {
  count = var.enable_postgres ? 1 : 0

  identifier          = "${var.cluster_name}-postgres"
  engine              = "postgres"
  engine_version      = "15"
  instance_class      = var.postgres_instance_class
  allocated_storage   = 20
  db_name             = "ubag"
  username            = "ubag"
  password            = var.postgres_password
  skip_final_snapshot = true
  publicly_accessible = false

  db_subnet_group_name = aws_db_subnet_group.ubag[0].name
}

# ---------------------------------------------------------------------------
# Optional: S3 bucket
# ---------------------------------------------------------------------------
resource "aws_s3_bucket" "ubag" {
  count  = var.enable_s3 ? 1 : 0
  bucket = var.s3_bucket_name

  tags = { Name = var.s3_bucket_name }
}

resource "aws_s3_bucket_versioning" "ubag" {
  count  = var.enable_s3 ? 1 : 0
  bucket = aws_s3_bucket.ubag[0].id

  versioning_configuration {
    status = "Enabled"
  }
}

# ---------------------------------------------------------------------------
# UBAG Helm module
# ---------------------------------------------------------------------------
module "ubag" {
  source = "../ubag"

  kube_config_path    = local_file.kubeconfig.filename
  kube_config_context = var.cluster_name
  release_name        = "ubag"
  chart_path          = "../../helm/ubag"
  namespace           = "ubag"
  create_namespace    = true

  ingress_enabled    = true
  ingress_host       = var.domain
  ingress_class_name = "nginx"

  image_tag = var.ubag_version

  manage_secret         = true
  ubag_app_secret       = var.ubag_app_secret
  ubag_postgres_dsn     = var.ubag_postgres_dsn
  ubag_minio_access_key = var.ubag_minio_access_key
  ubag_minio_secret_key = var.ubag_minio_secret_key
  ubag_webhook_secret   = var.ubag_webhook_secret

  depends_on = [local_file.kubeconfig]
}
