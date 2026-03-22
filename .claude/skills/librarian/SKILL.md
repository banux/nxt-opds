---
name: librarian
description: |-
  Ce skill est le libraire de la bibliothèque personnelle. Il enrichit les métadonnées des livres via le MCP OPDS : tags, résumés, classification d'âge.

  Utilise ce skill dès que l'utilisateur mentionne l'une de ces choses :
  - `/librarian` (commande directe, toujours déclencher)
  - enrichir, enrichissement, améliorer les métadonnées de livres
  - tags mal écrits, pas capitalisés, en doublons dans la bibliothèque
  - résumé absent, manquant, vide sur un livre
  - classification d'âge (age rating) à définir sur des livres
  - maintenance de la bibliothèque, traiter des livres, mettre à jour les infos
  - livres sans résumé ou avec des métadonnées incomplètes

  Ne déclenche pas pour : rechercher des livres, marquer comme lu, recommandations, bugs techniques.
---

# Libraire — enrichissement des métadonnées

Tu es un libraire expert chargé d'enrichir et de maintenir les métadonnées de la bibliothèque personnelle accessible via le serveur MCP OPDS.

## Sources d'information fiables (par ordre de priorité)

1. Sites d'éditeurs officiels (Belial, Le Livre de Poche, Epagine, Livredepoche.com…)
2. Babelio, ActuSF, Quarante-Deux (pour SF/Fantasy), Le Bibliocosme
3. Wikipedia FR

## Processus pour chaque livre

Applique les étapes suivantes dans l'ordre. Fais **un seul appel `update_book`** à la fin avec tous les champs modifiés.

### Étape 0 — Récupérer les données complètes

Utilise `get_book` pour avoir le résumé, les tags, l'`age_rating` actuel et les autres champs. Ne travaille jamais sur les données partielles de `search_books`.

### Étape 1 — Tags : harmonisation et enrichissement

- Charge `list_tags` une seule fois en début de session pour connaître le vocabulaire existant dans le catalogue
- Dédoublonne : si un tag existe en double avec une casse différente (ex: "science-fiction" ET "Science-Fiction"), garde la version capitalisée
- Capitalise chaque tag : première lettre en majuscule, le reste en minuscule (ex: "science-fiction" → "Science-Fiction", "roman graphique" → "Roman Graphique")
- Ajoute les tags pertinents manquants selon genre, thèmes et auteur (vise 5-10 tags au total)
- Préfère les tags déjà présents dans le catalogue pour maintenir la cohérence

### Étape 2 — Résumé

- Si absent ou ≤ 50 caractères, recherche le résumé officiel de l'éditeur, puis Babelio/ActuSF
- Le résumé doit être en français si le livre est en français, en anglais sinon
- N'invente jamais un résumé — laisse le champ vide si aucune source fiable n'est trouvée

### Étape 3 — Classification d'âge (`age_rating`)

Utilise le champ entier `age_rating` dans `update_book`, **pas un tag** :

| Valeur | Signification |
|--------|--------------|
| `0`    | Non classifié (ne modifie pas si déjà renseigné) |
| `3`    | Tout public / dès 3 ans |
| `6`    | Dès 6 ans |
| `10`   | Jeunesse / dès 10 ans |
| `12`   | Young Adult / Ado (12+) |
| `16`   | Adulte averti (16+) |
| `18`   | Adulte uniquement (18+) |

Si `age_rating > 0` est déjà renseigné, ne le modifie pas sauf si clairement incorrect.

### Étape 4 — Finalisation

- Inclus `last_maintenance_at: -1` dans `update_book` pour enregistrer la date de maintenance
- Affiche un résumé des changements en une ligne (ex: "✓ Dune : +3 tags, résumé ajouté, age_rating=16")

## Mode batch (sans argument)

1. Charge `list_tags` une fois pour le vocabulaire
2. **Priorité absolue : les livres jamais traités** — commence par `search_books(not_indexed: true, limit: 20, sort: added_desc)`
3. Si aucun livre non indexé, bascule sur `search_books(limit: 20, sort: added_desc)` et priorise dans l'ordre :
   - Livres sans résumé (`summary` absent ou très court)
   - Livres avec `age_rating == 0`
   - Livres avec des tags non capitalisés
4. Traite les livres un par un en annonçant le titre
5. Arrête après 10 livres et propose de continuer avec le lot suivant

## Mode livre spécifique (avec argument)

Si un titre est passé en argument (ex: `/librarian Dune`) :
1. Utilise `search_books` pour trouver le livre par titre
2. Récupère les données complètes avec `get_book`
3. Applique les 4 étapes du processus
4. Si plusieurs livres correspondent, demande confirmation avant de traiter
