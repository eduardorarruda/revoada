package senha

// comuns são senhas de 12+ caracteres que aparecem no topo de listas públicas de
// vazamento (em inglês e português). As curtas já caem na regra de comprimento.
// Comparação em minúsculas.
var comuns = func() map[string]struct{} {
	lista := []string{
		"123456789012", "1234567890123", "12345678901234", "123456789123", "1234567891011",
		"123123123123", "111111111111", "000000000000", "121212121212", "123456123456",
		"qwertyuiopas", "qwertyuiop12", "qwertyuiop123", "1qaz2wsx3edc", "1q2w3e4r5t6y",
		"zaq12wsxcde3", "qazwsxedcrfv", "asdfghjkl123", "zxcvbnm12345", "abcdefghijkl",
		"passwordpassword", "password1234", "password12345", "password123456", "password123!",
		"passw0rd1234", "p@ssw0rd1234", "p@ssword1234", "iloveyou1234", "letmein12345",
		"welcome12345", "welcome123456", "administrator", "administrador", "admin1234567",
		"admin123456789", "adminadmin12", "changeme1234", "default12345", "football1234",
		"baseball1234", "superman1234", "trustno11234", "sunshine1234", "princess1234",
		"monkey123456", "dragon123456", "master123456", "shadow123456", "michael12345",
		"senha1234567", "senha12345678", "senha123456789", "senhasenha12", "minhasenha123",
		"minhasenha1234", "mudar1234567", "mudar12345678", "trocar123456", "brasil123456",
		"brasil1234567", "flamengo1234", "corinthians1", "corinthians12", "corinthians123",
		"palmeiras123", "palmeiras1234", "saopaulo1234", "vasco1234567", "gremio123456",
		"cruzeiro1234", "internacional", "jesuscristo1", "deusefiel123", "deusefiel1234",
		"amor12345678", "teamo1234567", "familia12345", "felicidade12", "estrela12345",
		"qwerty123456", "qwerty1234567", "asdasdasdasd", "abc123abc123", "aaaaaa111111",
		"revoada12345", "revoada123456", "revoada@2026", "revoada2026!", "empresa12345",
		"firebird1234", "postgres1234", "masterkey123", "masterkey1234", "sysdba123456",
		"root12345678", "toor12345678", "database1234", "servidor1234", "senhadobanco",
	}
	m := make(map[string]struct{}, len(lista))
	for _, s := range lista {
		m[s] = struct{}{}
	}
	return m
}()
