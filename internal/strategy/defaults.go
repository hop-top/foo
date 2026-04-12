package strategy

// builtinStrategies are the default strategies shipped with foo.
var builtinStrategies = []Strategy{
	{
		Name:        "cot",
		Description: "Chain of Thought — step-by-step reasoning",
		Prefix: "Think through this problem step by step.\n" +
			"For each step:\n" +
			"1. State what you're considering\n" +
			"2. Explain your reasoning\n" +
			"3. Draw a conclusion before moving to the next step\n\n" +
			"After all steps, provide your final answer.",
	},
	{
		Name:        "tot",
		Description: "Tree of Thought — explore multiple solution paths",
		Prefix: "Explore multiple solution paths for this problem.\n" +
			"For each path:\n" +
			"1. Describe the approach\n" +
			"2. Work through it\n" +
			"3. Evaluate its strengths and weaknesses\n\n" +
			"After exploring at least 3 paths, select the best one and explain why.",
	},
	{
		Name:        "aot",
		Description: "Algorithm of Thought — algorithmic decomposition",
		Prefix: "Break this problem into an algorithmic sequence:\n" +
			"1. Define the input and desired output\n" +
			"2. Identify the core operation\n" +
			"3. Decompose into ordered sub-steps\n" +
			"4. Handle edge cases\n" +
			"5. Synthesize the result",
	},
	{
		Name:        "reflexion",
		Description: "Self-evaluate and improve your answer",
		Prefix: "Answer the following, then critique your own answer.",
		Suffix: "Now review your answer above:\n" +
			"- What assumptions did you make?\n" +
			"- What did you miss?\n" +
			"- What would you change?\n\n" +
			"Provide an improved final answer incorporating your critique.",
	},
	{
		Name:        "step-back",
		Description: "Abstract before solving",
		Prefix: "Before answering directly, step back and identify:\n" +
			"1. What broader principle or concept applies here?\n" +
			"2. What domain knowledge is relevant?\n" +
			"3. What analogous problems have known solutions?\n\n" +
			"Then use those insights to answer the specific question.",
	},
}
