terraform {
  source = "../modules/storage"
}

dependency "db" {
  config_path = "../db"
  skip_outputs = true
}
