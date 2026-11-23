package integration

func searchPreparationArguments(arguments []string) []string {
	return append(
		[]string{
			"--execute",
			"--output",
			"json",
			"--operator-id",
			"019dd226-440e-729a-a442-281aaf73ca30",
			"--operator-email",
			"operator@example.com",
			"--operator-name",
			"Search Test",
			"ops",
			"qa",
			"datagen",
			"search",
		},
		arguments...,
	)
}
